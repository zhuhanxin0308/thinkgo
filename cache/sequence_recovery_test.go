package cache

import (
	"context"
	"errors"
	"strconv"
	"testing"

	redisDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver/redis"
)

// 只有业务信封、尚无任何失效水位时，序列淘汰后的首次清理也必须覆盖先前的票号。
func TestFirstClearAfterRedisSequenceEviction(t *testing.T) {
	for _, scope := range []string{"global", "prefix"} {
		for _, depth := range []int{0, 2} {
			t.Run(scope+"/depth-"+strconv.Itoa(depth), func(t *testing.T) {
				var backend Driver = atomicOrderRedis(t)
				for range depth {
					namespaced, err := NewNamespaceDriver(backend, "tenant:", "")
					if err != nil {
						t.Fatal(err)
					}
					backend = namespaced
				}
				writer := NewCache(nil, backend)
				reader := NewCache(nil, backend)
				const key = "doc:written-before-eviction"
				for range 10 {
					if err := writer.Set(key, "old"); err != nil {
						t.Fatal(err)
					}
				}
				if err := backend.Delete(writer.fenceSequenceKey()); err != nil {
					t.Fatal(err)
				}
				var err error
				if scope == "global" {
					err = reader.Flush()
				} else {
					err = reader.ClearPrefix("doc:")
				}
				if err != nil {
					t.Fatal(err)
				}
				if value, found, err := reader.Get(key); found || err != nil {
					t.Fatalf("清理成功后仍可读到旧数据：value=%v found=%t err=%v", value, found, err)
				}
				if floor, err := reader.invalidationForKey(context.Background(), backend, key); err != nil || floor <= 10 {
					t.Fatalf("清理水位应超过已发票号：floor=%d err=%v", floor, err)
				}
			})
		}
	}
}

// allkeys 淘汰可以同时清掉两份记录。观测到旧信封但不能证明票号上界时，清理须在发布水位前失败。
func TestMissingRedisTicketHistoryRejectsClearBeforePublication(t *testing.T) {
	backend := atomicOrderRedis(t)
	store := NewCache(nil, backend)
	const key = "legacy:high"
	if err := backend.Set(key, newCacheValueEnvelope(100, "keep"), 0); err != nil {
		t.Fatal(err)
	}
	for _, clear := range []func() error{store.Flush, func() error { return store.ClearPrefix("legacy:") }} {
		if err := clear(); !errors.Is(err, ErrCacheFenceUnavailable) {
			t.Fatalf("缺少上界时清理误报成功：%v", err)
		}
		for _, metadata := range []string{store.fenceInvalidationKey(), store.scopedInvalidationKey(), store.fenceSequenceKey()} {
			if _, found, err := backend.Get(metadata); found || err != nil {
				t.Fatalf("失败前发布了水位：key=%q found=%t err=%v", metadata, found, err)
			}
		}
		if got, found, err := store.Get(key); !found || err != nil || got != "keep" {
			t.Fatalf("失败的清理改变了业务值：got=%v found=%t err=%v", got, found, err)
		}
	}
}

type childWriteDuringClear struct {
	*redisDriver.Redis
	calls int
	write func() error
}

func (d *childWriteDuringClear) ClearPrefixIfContext(ctx context.Context, prefix string, match func(string) bool, remove func(any) (bool, error)) error {
	d.calls++
	if d.calls == 2 {
		if err := d.write(); err != nil {
			return err
		}
	}
	return d.Redis.ClearPrefixIfContext(ctx, prefix, match, remove)
}

// 子空间的首个票号可能小于父空间已发布的水位，物理删除时还要重新核验空间归属。
func TestParentRedisClearPreservesNewlyCreatedChild(t *testing.T) {
	backend := &childWriteDuringClear{Redis: atomicOrderRedis(t).(*redisDriver.Redis)}
	parent := NewCache(nil, backend)
	childDriver, err := NewNamespaceDriver(backend, "child:", "")
	if err != nil {
		t.Fatal(err)
	}
	child := NewCache(nil, childDriver)
	backend.write = func() error { return child.Set("fresh", "keep") }
	if err := parent.Set("old", 1); err != nil {
		t.Fatal(err)
	}
	if err := parent.Flush(); !errors.Is(err, ErrCacheFenceUnavailable) {
		t.Fatalf("跨空间清理应报告未处理的独立时钟：%v", err)
	}
	if got, found, err := child.Get("fresh"); err != nil || !found || got != "keep" {
		t.Fatalf("父空间误删并发创建的子数据：got=%v found=%t err=%v", got, found, err)
	}
}

type loseTicketsAfterPreflight struct {
	*redisDriver.Redis
	armed bool
}

func (d *loseTicketsAfterPreflight) AllocateFenceGenerationContext(ctx context.Context, sequence, issued string, floor, minimumIssued uint64) (uint64, error) {
	if d.armed {
		d.armed = false
		for _, key := range []string{sequence, issued} {
			if err := d.Delete(key); err != nil {
				return 0, err
			}
		}
	}
	return d.Redis.AllocateFenceGenerationContext(ctx, sequence, issued, floor, minimumIssued)
}

// 预检读到的票号证据应在最终原子分配中仍然成立。
func TestClearRejectsLossOfWitnessAfterPreflight(t *testing.T) {
	for _, scope := range []string{"global", "prefix"} {
		t.Run(scope, func(t *testing.T) {
			raw := &loseTicketsAfterPreflight{Redis: atomicOrderRedis(t).(*redisDriver.Redis)}
			c := NewCache(nil, raw)
			if err := c.Set("doc:key", "keep"); err != nil {
				t.Fatal(err)
			}
			raw.armed = true
			var err error
			if scope == "global" {
				err = c.Flush()
			} else {
				err = c.ClearPrefix("doc:")
			}
			if !errors.Is(err, ErrCacheFenceUnavailable) {
				t.Fatalf("预检之后的票号丢失未报告：%v", err)
			}
			if got, found, err := c.Get("doc:key"); !found || err != nil || got != "keep" {
				t.Fatalf("失败的清理改变了原值：got=%v found=%t err=%v", got, found, err)
			}
		})
	}
}
