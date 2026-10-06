package driver

import (
	"testing"
	"time"
)

// TestMemoryAtomicTTLStartsAfterCallback 防止加载回调的耗时被扣除出新值的有效期。
func TestMemoryAtomicTTLStartsAfterCallback(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		name := "update"
		if guarded {
			name = "guarded"
		}
		t.Run(name, func(t *testing.T) {
			backend := NewMemory()
			const ttl = time.Minute
			var callbackFinished time.Time
			update := func(interface{}, bool) (interface{}, bool, error) {
				// 只推进时钟，不对调度速度设上限；断言检查存储的时间点而非立即读取是否命中。
				time.Sleep(time.Millisecond)
				callbackFinished = time.Now()
				return []int{1, 2, 3}, false, nil
			}
			if guarded {
				if acquired, err := backend.AcquireLock("lease", "owner", time.Hour); err != nil || !acquired {
					t.Fatalf("获取租约: %t %v", acquired, err)
				}
				if committed, err := backend.UpdateIfLockOwnerContext(t.Context(), "value", ttl, "lease", "owner", update); err != nil || !committed {
					t.Fatalf("提交: %t %v", committed, err)
				}
			} else if err := backend.Update("value", ttl, update); err != nil {
				t.Fatal(err)
			}
			backend.lock.RLock()
			expiry := backend.items["value"].expiry
			backend.lock.RUnlock()
			if expiry.Before(callbackFinished.Add(ttl)) {
				t.Fatalf("新 TTL 消耗了回调耗时: expiry=%v want >=%v", expiry, callbackFinished.Add(ttl))
			}
		})
	}
}

// TestMemoryAtomicPreserveTTLRemainsAbsolute 保留 TTL 的更新不得重新延长原记录期限。
func TestMemoryAtomicPreserveTTLRemainsAbsolute(t *testing.T) {
	backend := NewMemory()
	if err := backend.Set("value", "old", time.Hour); err != nil {
		t.Fatal(err)
	}
	original := backend.items["value"].expiry
	if err := backend.UpdatePreserveTTL("value", func(interface{}, bool) (interface{}, bool, error) { return "new", false, nil }); err != nil {
		t.Fatal(err)
	}
	if got := backend.items["value"].expiry; !got.Equal(original) {
		t.Fatalf("保留 TTL 被改变: %v -> %v", original, got)
	}
}
