package cache

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// UpdatePreserveTTL 让计数测试在真实原子提交前暂停，同时保留原有 TTL 契约。
func (driver *blockingIndependentUpdateDriver) UpdatePreserveTTL(key string, update func(interface{}, bool) (interface{}, bool, error)) error {
	if key == driver.target {
		driver.once.Do(func() { close(driver.started) })
		<-driver.release
	}
	return driver.Memory.UpdatePreserveTTL(key, update)
}

// UpdatePreserveTTLConditionally 对新增的原子 TTL 能力保留相同暂停点，防止提升能力后测试绕过交错。
func (driver *blockingIndependentUpdateDriver) UpdatePreserveTTLConditionally(key string, update func(interface{}, bool) (interface{}, bool, bool, error)) error {
	if key == driver.target {
		driver.once.Do(func() { close(driver.started) })
		<-driver.release
	}
	return driver.Memory.UpdatePreserveTTLConditionally(key, update)
}

// TestInvalidationDoesNotDeleteUnrelatedWrites 验证失效范围不能扩散到同一缓存中的其他业务或会话。
func TestInvalidationDoesNotDeleteUnrelatedWrites(t *testing.T) {
	for _, operation := range []string{"set", "update", "counter", "batch"} {
		for _, invalidation := range []string{"forget", "tag", "prefix", "same bucket"} {
			t.Run(operation+"/"+invalidation, func(t *testing.T) {
				backend := &blockingIndependentUpdateDriver{
					Memory: cacheDriver.NewMemory(), target: "session:active",
					started: make(chan struct{}), release: make(chan struct{}),
				}
				manager := NewCache(nil, backend)
				productKey := "product:1"
				if invalidation == "same bucket" {
					for index := 0; ; index++ {
						candidate := fmt.Sprintf("product:%d", index)
						if keyInvalidationBucket(candidate) == keyInvalidationBucket(backend.target) {
							productKey = candidate
							break
						}
					}
				}
				tagged, err := manager.Tag("products")
				if err != nil {
					t.Fatal(err)
				}
				if err = tagged.Set(productKey, "old", time.Minute); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					switch operation {
					case "set":
						done <- manager.Set(backend.target, "saved", time.Minute)
					case "update":
						done <- manager.Update(backend.target, time.Minute, func(interface{}, bool) (interface{}, bool, error) {
							return "saved", false, nil
						})
					case "counter":
						_, counterErr := manager.Inc(backend.target, 1)
						done <- counterErr
					case "batch":
						done <- manager.SetMany(map[string]interface{}{backend.target: "saved", "session:second": "second"}, time.Minute)
					}
				}()
				select {
				case <-backend.started:
				case <-time.After(time.Second):
					close(backend.release)
					<-done
					t.Fatal("写入没有到达失效前的暂停点")
				}
				if invalidation == "forget" || invalidation == "same bucket" {
					err = manager.Forget(productKey)
				} else if invalidation == "prefix" {
					err = manager.ClearPrefix("product:")
				} else {
					err = tagged.Flush()
				}
				close(backend.release)
				writeErr := <-done
				if err != nil || writeErr != nil {
					t.Fatalf("无关缓存失效不得破坏并发写入: invalidate=%v write=%v", err, writeErr)
				}
				if _, found, getErr := manager.Get(backend.target); getErr != nil || !found {
					t.Fatalf("无关失效删除了已提交数据: found=%t err=%v", found, getErr)
				}
			})
		}
	}
}

// TestInvalidatedLateEnvelopeIsNeverVisible 模拟旧写者完成底层写入后暂停在后置 fencing 校验之前。
func TestInvalidatedLateEnvelopeIsNeverVisible(t *testing.T) {
	for _, kind := range []string{"forget", "prefix", "flush"} {
		for _, read := range []string{"get", "has", "batch", "update", "counter"} {
			t.Run(kind+"/"+read, func(t *testing.T) {
				backend := cacheDriver.NewMemory()
				manager := NewCache(nil, backend)
				const key = "session:late"
				generation, err := manager.nextFenceGeneration(backend)
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "forget":
					err = manager.Forget(key)
				case "prefix":
					err = manager.ClearPrefix("session:")
				case "flush":
					err = manager.Flush()
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = backend.Set(key, newCacheValueEnvelope(generation, 9), time.Minute); err != nil {
					t.Fatal(err)
				}
				switch read {
				case "get":
					if value, found, err := manager.Get(key); err != nil || found {
						t.Fatalf("失效旧值可见: %v %t %v", value, found, err)
					}
				case "has":
					if found, err := manager.Has(key); err != nil || found {
						t.Fatalf("失效旧值被报告存在: %t %v", found, err)
					}
				case "batch":
					if values, err := manager.GetMany([]string{key}); err != nil || len(values) != 0 {
						t.Fatalf("批量读取泄露失效旧值: %v %v", values, err)
					}
				case "update":
					if err := manager.Update(key, time.Minute, func(value interface{}, found bool) (interface{}, bool, error) {
						if found {
							return nil, false, errors.New("新更新读取了已失效的旧值")
						}
						return "fresh", false, nil
					}); err != nil {
						t.Fatal(err)
					}
				case "counter":
					if count, err := manager.Inc(key, 1); err != nil || count != 1 {
						t.Fatalf("计数从失效旧值累加: %d %v", count, err)
					}
				}
			})
		}
	}
}

type blockingConditionalClearDriver struct {
	*cacheDriver.Memory
	started chan struct{}
	release chan struct{}
}

func (d *blockingConditionalClearDriver) ClearPrefixIfContext(ctx context.Context, prefix string, match func(string) bool, remove func(interface{}) (bool, error)) error {
	close(d.started)
	<-d.release
	return d.Memory.ClearPrefixIfContext(ctx, prefix, match, remove)
}

// TestClearPreservesNewerConcurrentWrites 验证清理发布边界之后成功写入的值不会被迟到扫描误删。
func TestClearPreservesNewerConcurrentWrites(t *testing.T) {
	for _, prefix := range []string{"", "session:"} {
		t.Run(prefix, func(t *testing.T) {
			backend := &blockingConditionalClearDriver{Memory: cacheDriver.NewMemory(), started: make(chan struct{}), release: make(chan struct{})}
			manager := NewCache(nil, backend)
			if err := manager.Set("session:old", "old"); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if prefix == "" {
					done <- manager.Flush()
				} else {
					done <- manager.ClearPrefix(prefix)
				}
			}()
			<-backend.started
			writeErr := manager.Set("session:old", "new")
			close(backend.release)
			if err := <-done; err != nil || writeErr != nil {
				t.Fatalf("清理与新写入失败: %v %v", err, writeErr)
			}
			if value, found, err := manager.Get("session:old"); err != nil || !found || value != "new" {
				t.Fatalf("清理误删新值: %v %t %v", value, found, err)
			}
		})
	}
}

// TestClearRejectsLateWritesInItsScope 验证清理完成后旧代写入不能重新出现，范围外写入仍可提交。
func TestClearRejectsLateWritesInItsScope(t *testing.T) {
	for _, kind := range []string{"prefix", "flush"} {
		t.Run(kind, func(t *testing.T) {
			backend := &blockingIndependentUpdateDriver{
				Memory: cacheDriver.NewMemory(), target: "session:active",
				started: make(chan struct{}), release: make(chan struct{}),
			}
			manager := NewCache(nil, backend)
			done := make(chan error, 1)
			go func() { done <- manager.Set(backend.target, "stale", time.Minute) }()
			select {
			case <-backend.started:
			case <-time.After(time.Second):
				close(backend.release)
				<-done
				t.Fatal("旧写入没有进入暂停点")
			}
			var err error
			if kind == "prefix" {
				err = manager.ClearPrefixContext(context.Background(), "session:")
			} else {
				err = manager.Flush()
			}
			close(backend.release)
			writeErr := <-done
			if err != nil || !errors.Is(writeErr, ErrCacheLockLost) {
				t.Fatalf("跨过清理边界的旧写入必须失败: clear=%v write=%v", err, writeErr)
			}
			if _, found, getErr := manager.Get(backend.target); getErr != nil || found {
				t.Fatalf("清理后旧值重新出现: found=%t err=%v", found, getErr)
			}
		})
	}
}
