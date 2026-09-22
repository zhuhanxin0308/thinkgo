package cache

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver"
)

// TestAtomicOriginHandlesDelayedInvalidation 模拟失效已领票但暂停发布，包括旧租约持有者恢复。
func TestAtomicOriginHandlesDelayedInvalidation(t *testing.T) {
	for _, base := range []string{"old", "fresh", "inherited"} {
		for _, operation := range []string{"update", "increment", "remove"} {
			t.Run(base+"/"+operation, func(t *testing.T) {
				backend := cacheDriver.NewMemory()
				manager := NewCache(nil, backend)
				wrapped := &reorderedAtomicDriver{Driver: backend, target: "delayed"}
				slow := NewCache(nil, wrapped)
				wrapped.before = func() {
					if err := manager.Set(wrapped.target, int64(10)); err != nil {
						t.Fatal(err)
					}
				}
				wrapped.after = func(err error) {
					if !errors.Is(err, errCacheAtomicFenceStale) {
						t.Fatalf("测试未形成票据乱序: %v", err)
					}
					fence, err := manager.nextFenceGeneration(backend)
					if err != nil {
						t.Fatal(err)
					}
					if base == "fresh" {
						err = manager.Set(wrapped.target, int64(10))
					} else if base == "inherited" {
						var generation uint64
						generation, err = manager.nextFenceGeneration(backend)
						if err == nil {
							err = backend.Set(wrapped.target, newAtomicCacheValueEnvelope(generation, fence-1, int64(10)), time.Minute)
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					wrapped.after = func(commitErr error) {
						if commitErr != nil {
							t.Fatalf("原子提交失败: %v", commitErr)
						}
						if err := backend.Set(manager.fenceInvalidationKey(), strconv.FormatUint(fence, 10), 0); err != nil {
							t.Fatal(err)
						}
						// 写者尚未后置回滚，读取本身必须立刻隐藏已经失效的来源。
						value, found, getErr := manager.Get(wrapped.target)
						if getErr != nil || found != (base == "fresh" && operation != "remove") || found && value != int64(11) {
							t.Fatalf("延迟水位下可见性错误: value=%v found=%t err=%v", value, found, getErr)
						}
					}
				}
				var err error
				if operation == "increment" {
					_, err = slow.Inc(wrapped.target, 1)
				} else {
					err = slow.Update(wrapped.target, time.Minute, func(value interface{}, found bool) (interface{}, bool, error) {
						if !found || value != int64(10) {
							t.Errorf("原子操作未读取实际基值: %v %t", value, found)
						}
						return int64(11), operation == "remove", nil
					})
				}
				if base == "fresh" && err != nil || base != "fresh" && !errors.Is(err, ErrCacheLockLost) {
					t.Fatalf("延迟水位错误处理不符合基值来源: %v", err)
				}
			})
		}
	}
}

// TestAtomicOriginDoesNotSurviveRedisWatchConflict 验证失败尝试的较新来源不能用于随后消失的基值。
func TestAtomicOriginDoesNotSurviveRedisWatchConflict(t *testing.T) {
	assertAtomicWatchMissingOrigin(t, atomicOrderRedis(t))
}

func assertAtomicWatchMissingOrigin(t *testing.T, backend Driver) {
	t.Helper()
	manager := NewCache(nil, backend)
	wrapped := &reorderedAtomicDriver{Driver: backend, target: "watch-missing-origin"}
	slow := NewCache(nil, wrapped)
	wrapped.before = func() {
		if err := manager.Forget(wrapped.target); err != nil {
			t.Fatal(err)
		}
		if err := manager.Set(wrapped.target, int64(10)); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	err := slow.Update(wrapped.target, time.Minute, func(value interface{}, found bool) (interface{}, bool, error) {
		calls++
		if !found || value != int64(10) {
			t.Errorf("失败 WATCH 的 Origin 泄漏至缺失基值: value=%v found=%t", value, found)
		}
		// 模拟事务提交前 TTL 到期，物理键消失，但没有赋予旧操作新的可见性屏障。
		if err := backend.Delete(wrapped.target); err != nil {
			return nil, false, err
		}
		return int64(11), false, nil
	})
	if !errors.Is(err, ErrCacheLockLost) || calls != 1 {
		t.Fatalf("WATCH 重放继承了失败尝试的 Origin: calls=%d err=%v", calls, err)
	}
	if value, found, err := manager.Get(wrapped.target); err != nil || found {
		t.Fatalf("WATCH 重放复活了已消失的值: %v %t %v", value, found, err)
	}
}

// TestAtomicEnvelopeOriginValidation 覆盖旧信封兼容、JSON 精度和来源字段失败关闭。
func TestAtomicEnvelopeOriginValidation(t *testing.T) {
	for _, raw := range []interface{}{
		newCacheValueEnvelope(3, int64(10)),
		newAtomicCacheValueEnvelope(5, 3, int64(10)),
	} {
		encoded, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]interface{}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		for _, input := range []interface{}{raw, decoded} {
			value, generation, origin, found, err := decodeCacheValueFence(input)
			if err != nil || !found || generation < origin || origin != 3 || value != int64(10) {
				t.Fatalf("合法信封未兼容: %v %d %d %t %v", value, generation, origin, found, err)
			}
		}
	}
	for _, origin := range []interface{}{"0", "6", "bad", "", 3, nil} {
		raw := map[string]interface{}{"__thinkgo_cache_envelope": cacheValueEnvelopeMarker, "generation": "5", "origin": origin, "value": "value"}
		if _, _, _, _, err := decodeCacheValueFence(raw); !errors.Is(err, ErrCorruptCacheEnvelope) {
			t.Fatalf("非法来源字段未拒绝: %v %v", origin, err)
		}
	}
	invalid := newAtomicCacheValueEnvelope(5, 6, "value")
	for _, raw := range []interface{}{invalid, &invalid, map[string]interface{}{
		"__thinkgo_cache_envelope": cacheValueEnvelopeMarker, "generation": "5", "origin": "3", "value": "value", "unknown": true,
	}} {
		if _, _, _, _, err := decodeCacheValueFence(raw); !errors.Is(err, ErrCorruptCacheEnvelope) {
			t.Fatalf("损坏信封未失败关闭: %v %v", raw, err)
		}
	}
}

// TestInvalidationDeletesRebasedOldOrigin 验证单键、标签、前缀与全清理都按来源删除，不能被较新提交身份遮蔽。
func TestInvalidationDeletesRebasedOldOrigin(t *testing.T) {
	for _, kind := range []string{"forget", "tag", "prefix", "flush"} {
		t.Run(kind, func(t *testing.T) {
			backend := cacheDriver.NewMemory()
			manager := NewCache(nil, backend)
			tagged, err := manager.Tag("origin-test")
			if err != nil {
				t.Fatal(err)
			}
			const key = "session:rebased"
			if err := tagged.Set(key, "old", time.Minute); err != nil {
				t.Fatal(err)
			}
			raw, _, err := backend.Get(key)
			if err != nil {
				t.Fatal(err)
			}
			_, origin, _, err := decodeCacheValueEnvelope(raw)
			if err != nil {
				t.Fatal(err)
			}
			// 模拟尚未公开的失效票据先于重新领票的提交身份，来源仍旧。
			if err := backend.Set(key, newAtomicCacheValueEnvelope(origin+2, origin, "rebased"), time.Minute); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "forget":
				err = manager.Forget(key)
			case "tag":
				err = tagged.Flush()
			case "prefix":
				err = manager.ClearPrefix("session:")
			case "flush":
				err = manager.Flush()
			}
			if err != nil {
				t.Fatal(err)
			}
			if value, found, err := backend.Get(key); err != nil || found {
				t.Fatalf("失效被较新的提交身份遮蔽，物理旧值未清除: %v %t %v", value, found, err)
			}
		})
	}
}
