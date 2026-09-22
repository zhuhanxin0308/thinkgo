package driver

import (
	"errors"
	"testing"
	"time"
)

type conditionalTTLUpdater interface {
	UpdatePreserveTTLConditionally(string, func(interface{}, bool) (interface{}, bool, bool, error)) error
}

// TestAtomicTTLPolicyUsesSameCommit 验证逻辑失效后的新值在原子提交内重置 TTL，失败不能改变原值或有效期。
func TestAtomicTTLPolicyUsesSameCommit(t *testing.T) {
	file, _ := newTestFileDriver(t)
	for name, backend := range map[string]atomicCacheDriver{"memory": NewMemory(), "file": file} {
		t.Run(name, func(t *testing.T) {
			updater, ok := backend.(conditionalTTLUpdater)
			if !ok {
				t.Fatal("驱动缺少原子 TTL 决策能力")
			}
			expiry := func() time.Time {
				if memory, ok := backend.(*Memory); ok {
					memory.lock.RLock()
					defer memory.lock.RUnlock()
					return memory.items["value"].expiry
				}
				item, found, err := file.readItemLocked("value", time.Now())
				if err != nil || !found {
					t.Fatalf("读取持久 TTL 失败: %t %v", found, err)
				}
				return item.Expiry
			}
			if err := backend.Set("value", "old", time.Minute); err != nil {
				t.Fatal(err)
			}
			originalExpiry := expiry()
			for _, preserve := range []bool{true, false} {
				if err := updater.UpdatePreserveTTLConditionally("value", func(value interface{}, found bool) (interface{}, bool, bool, error) {
					if !found || value != "old" {
						t.Fatalf("TTL 决策没有读取同一原子边界内的当前值: %v %t", value, found)
					}
					return "old", false, preserve, nil
				}); err != nil {
					t.Fatal(err)
				}
				if preserve && !expiry().Equal(originalExpiry) || !preserve && !expiry().IsZero() {
					t.Fatalf("TTL 决策没有随值提交: preserve=%t expiry=%v", preserve, expiry())
				}
			}
			failure := errors.New("业务拒绝提交")
			if err := updater.UpdatePreserveTTLConditionally("value", func(interface{}, bool) (interface{}, bool, bool, error) {
				return "wrong", true, false, failure
			}); !errors.Is(err, failure) {
				t.Fatalf("回调失败没有传播: %v", err)
			}
			if value, found, err := backend.Get("value"); err != nil || !found || value != "old" || !expiry().IsZero() {
				t.Fatalf("失败改变了值或 TTL: %v %t %v", value, found, err)
			}
			if err := updater.UpdatePreserveTTLConditionally("value", nil); !errors.Is(err, ErrNilAtomicUpdate) {
				t.Fatalf("空回调没有失败关闭: %v", err)
			}
			if err := updater.UpdatePreserveTTLConditionally("value", func(interface{}, bool) (interface{}, bool, bool, error) {
				return nil, true, true, nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, found, err := backend.Get("value"); err != nil || found {
				t.Fatalf("删除优先于 TTL 保留: %t %v", found, err)
			}
			if err := updater.UpdatePreserveTTLConditionally("value", func(_ interface{}, found bool) (interface{}, bool, bool, error) {
				if found {
					t.Fatal("删除后的键不应存在")
				}
				return "created", false, true, nil
			}); err != nil || !expiry().IsZero() {
				t.Fatalf("缺失项创建必须永久有效: %v", err)
			}
		})
	}
}
