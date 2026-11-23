package cache

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// 同一套业务入口同时供内存驱动、模拟 Redis 和 CI 的真实 Redis 使用。
func TestWritesRejectEqualInvalidation(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		assertWritesRejectEqualInvalidation(t, NewCache(nil, cacheDriver.NewMemory()))
	})
	t.Run("redis", func(t *testing.T) {
		assertWritesRejectEqualInvalidation(t, NewCache(nil, atomicOrderRedis(t)))
	})
}

func assertWritesRejectEqualInvalidation(t *testing.T, manager *Cache) {
	t.Helper()
	backend, release, err := manager.driver()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, operation := range []string{"set", "batch", "update", "inc", "dec", "remember", "guarded", "tagged"} {
		t.Run(operation, func(t *testing.T) {
			key := "retained-fence:" + operation
			const other = "a:outside-fence"
			if err := backend.Delete(manager.fenceSequenceKey()); err != nil {
				t.Fatal(err)
			}
			if err := manager.Forget(key); err != nil {
				t.Fatal(err)
			}
			// 模拟只有序列键被淘汰。刚发布的精确水位和其他业务键依然有效。
			if err := backend.Delete(manager.fenceSequenceKey()); err != nil {
				t.Fatal(err)
			}
			var writeErr error
			switch operation {
			case "set":
				writeErr = manager.Set(key, "new", 0)
			case "batch":
				if err := backend.Set(other, "legacy", 0); err != nil {
					t.Fatal(err)
				}
				writeErr = manager.SetMany(map[string]interface{}{other: "batch", key: "new"}, 0)
				if got, found, err := manager.Get(other); err != nil || !found || got != "legacy" {
					t.Errorf("已存在的失效水位不应让失败批次替换无关裸值：%v %t %v", got, found, err)
				}
			case "update":
				writeErr = manager.Update(key, 0, func(interface{}, bool) (interface{}, bool, error) {
					t.Error("已读到等于起始票号的水位时不应执行业务回调")
					return "new", false, nil
				})
			case "inc":
				_, writeErr = manager.Inc(key, 1)
			case "dec":
				_, writeErr = manager.Dec(key, 1)
			case "remember", "guarded":
				load := func() (interface{}, error) { return "new", nil }
				if operation == "remember" {
					_, writeErr = manager.Remember(key, 0, load)
				} else {
					_, writeErr = manager.RememberWithLock(key, 0, time.Minute, load)
				}
			case "tagged":
				tagged, err := manager.Tag("equal-fence")
				if err != nil {
					t.Fatal(err)
				}
				writeErr = tagged.Set(key, "new", 0)
				members, err := manager.tagMembersLockedContext(t.Context(), backend, "equal-fence")
				if err != nil || len(members) != 0 {
					t.Errorf("失败的标签写入保留了正向成员：%v %v", members, err)
				}
			}
			if !errors.Is(writeErr, ErrCacheLockLost) {
				t.Errorf("等于正水位的写入应报告丢失代际：%v", writeErr)
			}
			if value, found, err := manager.Get(key); err != nil || found {
				t.Fatalf("失效值不能重新可见：%v %t %v", value, found, err)
			}
			if err := manager.Set(key, "fresh", 0); err != nil {
				t.Fatal(err)
			}
			if value, found, err := manager.Get(key); err != nil || !found || value != "fresh" {
				t.Fatalf("较新代际应可见：%v %t %v", value, found, err)
			}
		})
	}
}

// 重新领票后的提交代际可高于来源。另一个重号写者的来源仍有效，不能按提交代际删除。
func TestEqualInvalidationPreservesReusedCommit(t *testing.T) {
	for _, operation := range []string{"update", "inc"} {
		for _, equal := range []string{"origin", "commit", "overlap", "different-origin"} {
			t.Run(operation+"/"+equal, func(t *testing.T) {
				assertReusedCommitPreserved(t, operation, equal)
			})
		}
	}
}

func assertReusedCommitPreserved(t *testing.T, operation, equal string) {
	t.Helper()
	const key = "same-commit"
	backend := cacheDriver.NewMemory()
	other := NewCache(nil, backend)
	wrapped := &reorderedAtomicDriver{Driver: backend, target: key}
	manager := NewCache(nil, wrapped)
	wrapped.before = func() {
		if err := other.Set(key, int64(10), 0); err != nil {
			t.Fatal(err)
		}
	}
	replaceCommit := func(commitErr error) {
		if commitErr != nil {
			t.Fatal(commitErr)
		}
		raw, _, err := backend.Get(key)
		if err != nil {
			t.Fatal(err)
		}
		_, commit, origin, _, err := decodeCacheValueFence(raw)
		if err != nil || commit <= origin || origin == 0 {
			t.Fatalf("交错场景应产生高于来源的提交票：%d %d %v", commit, origin, err)
		}
		watermark := origin
		if equal == "commit" {
			watermark = commit
		} else if equal == "different-origin" {
			watermark = origin + 1
		}
		if err := backend.Set(other.fenceInvalidationKey(), strconv.FormatUint(watermark, 10), 0); err != nil {
			t.Fatal(err)
		}
		if equal == "overlap" {
			if err := other.publishKeyInvalidation(t.Context(), backend, []string{key}, commit+1, tagMutationLease{}); err != nil {
				t.Fatal(err)
			}
		}
		if err := backend.Set(key, newCacheValueEnvelope(commit, "other"), 0); err != nil {
			t.Fatal(err)
		}
	}
	wrapped.after = func(firstErr error) {
		if !errors.Is(firstErr, errCacheAtomicFenceStale) {
			t.Fatalf("首次尝试应先重新领票：%v", firstErr)
		}
		if equal == "different-origin" {
			// 在来源与后续提交票之间留出独立水位，后验补偿只能依靠信封自己的来源证明。
			if _, err := other.nextFenceGeneration(backend); err != nil {
				t.Fatal(err)
			}
		}
		wrapped.after = replaceCommit
	}
	var err error
	if operation == "inc" {
		_, err = manager.Inc(key, 1)
	} else {
		err = manager.Update(key, 0, func(interface{}, bool) (interface{}, bool, error) { return int64(11), false, nil })
	}
	if !errors.Is(err, ErrCacheLockLost) {
		t.Errorf("来源已失效的提交应报告失败：%v", err)
	}
	raw, found, err := backend.Get(key)
	if err != nil || !found {
		t.Fatalf("补偿删除擦除了同代其他写者：%t %v", found, err)
	}
	value, _, _, err := decodeCacheValueEnvelope(raw)
	if err != nil || value != "other" {
		t.Fatalf("同代其他写者的原始值改变：%v %v", value, err)
	}
	wantVisible := equal == "origin" || equal == "different-origin"
	if value, found, err := other.Get(key); err != nil || found != wantVisible || found && value != "other" {
		t.Errorf("实际来源不符的可见性：%v %t %v", value, found, err)
	}
}

func TestBatchRollbackPreservesDifferentOriginAtSameCommit(t *testing.T) {
	const key = "same-commit"
	backend := cacheDriver.NewMemory()
	reader := NewCache(nil, backend)
	if err := reader.Set(key, "previous", 0); err != nil {
		t.Fatal(err)
	}
	lostReply := errors.New("提交应答丢失")
	wrapped := &batchRollbackFaultDriver{Driver: backend, afterCommit: true, failure: lostReply}
	manager := NewCache(nil, wrapped)
	wrapped.beforeReply = func() {
		raw, _, err := backend.Get(key)
		if err != nil {
			t.Fatal(err)
		}
		_, commit, _, err := decodeCacheValueEnvelope(raw)
		if err != nil || commit < 2 {
			t.Fatalf("无法建立两个不同的正代际：%d %v", commit, err)
		}
		// 无失效记录时，提交号相同但来源不同已经能证明当前值属于另一项原子写入。
		if err := backend.Set(key, newAtomicCacheValueEnvelope(commit, commit-1, "other"), 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.SetMany(map[string]interface{}{key: "batch", "z-fail": "trigger"}, 0); !errors.Is(err, lostReply) {
		t.Fatalf("必须保留原始后端错误：%v", err)
	}
	if value, found, err := reader.Get(key); err != nil || !found || value != "other" {
		t.Fatalf("回滚覆盖了他人来源的同代记录：%v %t %v", value, found, err)
	}
}

// 首项出现普通失效，末项随后暴露重号；同批其他键的票据此时也不能用来证明写入者。
func TestLateEqualBatchFencePreservesIndistinguishableWriter(t *testing.T) {
	for _, mode := range []string{"confirmed", "lost-reply", "canceled", "unreadable", "overlap"} {
		t.Run(mode, func(t *testing.T) {
			assertLateEqualBatchFence(t, mode)
		})
	}
}

type batchWatermarkReadFailure struct {
	*batchRollbackFaultDriver
	readErr error
}

func (d *batchWatermarkReadFailure) Get(key string) (interface{}, bool, error) {
	if d.readErr != nil && strings.HasPrefix(key, cacheFencePrefix) {
		return nil, false, d.readErr
	}
	return d.Driver.Get(key)
}

func assertLateEqualBatchFence(t *testing.T, mode string) {
	t.Helper()
	const unrelated = "other-owner"
	backend := cacheDriver.NewMemory()
	writer := NewCache(nil, backend)
	wrapped := &batchWatermarkReadFailure{batchRollbackFaultDriver: &batchRollbackFaultDriver{Driver: backend, afterCommit: true}}
	manager := NewCache(nil, wrapped)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var otherErr error
	if mode == "lost-reply" || mode == "unreadable" {
		otherErr = errors.New("提交应答丢失")
		wrapped.failure = otherErr
	} else if mode == "canceled" {
		otherErr = context.Canceled
	}
	if err := backend.Set(unrelated, "previous", 0); err != nil {
		t.Fatal(err)
	}
	wrapped.beforeReply = func() {
		raw, _, err := backend.Get(unrelated)
		if err != nil {
			t.Fatal(err)
		}
		_, generation, _, err := decodeCacheValueEnvelope(raw)
		if err != nil || generation == 0 {
			t.Fatalf("缺少已提交的批次代际：%v", err)
		}
		// 竞争者提交相同票号、值和永久 TTL，旧线协议无法区分两次写入。
		if err := backend.Set(unrelated, newCacheValueEnvelope(generation, "same"), 0); err != nil {
			t.Fatal(err)
		}
		if err := writer.Forget("a-stale"); err != nil {
			t.Fatal(err)
		}
		if mode == "overlap" {
			if err := backend.Delete(writer.fenceSequenceKey()); err != nil {
				t.Fatal(err)
			}
			if err := writer.ClearPrefix("z-"); err != nil {
				t.Fatal(err)
			}
			if err := writer.Forget("z-fail"); err != nil {
				t.Fatal(err)
			}
		} else if err := writer.publishKeyInvalidation(t.Context(), backend, []string{"z-fail"}, generation, tagMutationLease{}); err != nil {
			t.Fatal(err)
		}
		if mode == "canceled" {
			cancel()
		} else if mode == "unreadable" {
			wrapped.readErr = errors.New("补偿时无法读取失效水位")
		}
	}
	err := manager.SetManyContext(ctx, map[string]interface{}{"a-stale": "stale", unrelated: "same", "z-fail": "hidden"}, 0)
	wantFenceErr := ErrCacheLockLost
	if mode == "unreadable" {
		wantFenceErr = wrapped.readErr
	}
	if !errors.Is(err, wantFenceErr) || otherErr != nil && !errors.Is(err, otherErr) {
		t.Errorf("已失效的批次应保留重号和原始错误：%v", err)
	}
	if value, found, err := writer.Get(unrelated); err != nil || !found || value != "same" {
		t.Fatalf("无权回滚同代其他写者：%v %t %v", value, found, err)
	}
	if value, found, err := writer.Get("z-fail"); err != nil || found {
		t.Fatalf("等于水位的批次值不应可见：%v %t %v", value, found, err)
	}
}

// 被高提交代际包装的旧来源不可用于延续写入，重领票也不能让它跨过相等的正水位。
func TestEqualInvalidationRejectsInvisibleHighCommit(t *testing.T) {
	const key = "stale-origin"
	for _, operation := range []string{"update", "inc"} {
		t.Run(operation, func(t *testing.T) {
			backend := cacheDriver.NewMemory()
			manager := NewCache(nil, backend)
			if err := backend.Set(manager.fenceInvalidationKey(), "1", 0); err != nil {
				t.Fatal(err)
			}
			if err := backend.Set(key, newAtomicCacheValueEnvelope(maxCacheAtomicRebaseAttempts+1, 1, int64(20)), 0); err != nil {
				t.Fatal(err)
			}
			var err error
			if operation == "inc" {
				_, err = manager.Inc(key, 1)
			} else {
				err = manager.Update(key, 0, func(interface{}, bool) (interface{}, bool, error) {
					t.Fatal("失效来源不应进入业务回调")
					return nil, false, nil
				})
			}
			if !errors.Is(err, ErrCacheLockLost) {
				t.Fatalf("应返回已知的失效错误：%v", err)
			}
			if ticket, err := manager.nextFenceGeneration(backend); err != nil || ticket != 2 {
				t.Fatalf("已确定失效后仍重领票号：%d %v", ticket, err)
			}
		})
	}
}
