package cache

import (
	"errors"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver"
)

// TestNamespaceConditionalTTLForwardsAtomicPolicy 验证多层命名空间转发同一次原子 TTL 决策，并拒绝旧驱动模拟。
func TestNamespaceConditionalTTLForwardsAtomicPolicy(t *testing.T) {
	backend := cacheDriver.NewMemory()
	outer, err := NewNamespaceDriver(backend, "outer:", "tag:")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := NewNamespaceDriver(outer, "inner:", "tag:")
	if err != nil {
		t.Fatal(err)
	}
	updater, ok := interface{}(inner).(interface {
		UpdatePreserveTTLConditionally(string, func(interface{}, bool) (interface{}, bool, bool, error)) error
	})
	if !ok {
		t.Fatal("命名空间缺少原子 TTL 决策转发")
	}
	if err := inner.Set("value", "old", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := updater.UpdatePreserveTTLConditionally("value", func(value interface{}, found bool) (interface{}, bool, bool, error) {
		if !found || value != "old" {
			t.Fatalf("回调未读取逻辑键对应的物理值: %v %t", value, found)
		}
		return "new", false, false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if value, found, err := backend.Get("outer:inner:value"); err != nil || !found || value != "new" {
		t.Fatalf("TTL 决策修改了错误命名空间: %v %t %v", value, found, err)
	}
	legacy, err := NewNamespaceDriver(struct{ Driver }{backend}, "legacy:", "tag:")
	if err != nil {
		t.Fatal(err)
	}
	legacyUpdater := interface{}(legacy).(interface {
		UpdatePreserveTTLConditionally(string, func(interface{}, bool) (interface{}, bool, bool, error)) error
	})
	if err := legacyUpdater.UpdatePreserveTTLConditionally("value", func(interface{}, bool) (interface{}, bool, bool, error) {
		t.Fatal("不支持原子 TTL 的驱动不应执行业务回调")
		return "wrong", false, false, nil
	}); !errors.Is(err, ErrCacheAtomicUpdateUnsupported) {
		t.Fatalf("旧驱动必须明确失败: %v", err)
	}
}

// TestNamespaceConditionalTTLKeepsLegacyAtomicPaths 旧驱动保留同一次原子更新可保证的路径，重置现存 TTL 则拒绝。
func TestNamespaceConditionalTTLKeepsLegacyAtomicPaths(t *testing.T) {
	backend := cacheDriver.NewMemory()
	legacy := struct {
		Driver
		TTLAtomicUpdater
	}{backend, backend}
	namespaced, err := NewNamespaceDriver(legacy, "legacy:", "tag:")
	if err != nil {
		t.Fatal(err)
	}
	if err := namespaced.Set("value", "old", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := namespaced.UpdatePreserveTTLConditionally("value", func(value interface{}, found bool) (interface{}, bool, bool, error) {
		if !found || value != "old" {
			t.Fatalf("旧驱动没有读取当前值: %v %t", value, found)
		}
		return "preserved", false, true, nil
	}); err != nil {
		t.Fatalf("命中值的保留 TTL 路径不应退化: %v", err)
	}
	if err := namespaced.UpdatePreserveTTLConditionally("value", func(interface{}, bool) (interface{}, bool, bool, error) {
		return "wrong", false, false, nil
	}); !errors.Is(err, ErrCacheAtomicUpdateUnsupported) {
		t.Fatalf("旧驱动不能拆成多步重置 TTL: %v", err)
	}
	if value, found, err := namespaced.Get("value"); err != nil || !found || value != "preserved" {
		t.Fatalf("被拒绝的 TTL 决策改变原值: %v %t %v", value, found, err)
	}
	if err := namespaced.UpdatePreserveTTLConditionally("value", nil); !errors.Is(err, ErrNilCacheUpdate) {
		t.Fatalf("旧驱动空回调必须失败关闭: %v", err)
	}
	if err := namespaced.UpdatePreserveTTLConditionally("value", func(interface{}, bool) (interface{}, bool, bool, error) {
		return nil, true, false, nil
	}); err != nil {
		t.Fatalf("旧驱动的原子删除不应退化: %v", err)
	}
	if err := namespaced.UpdatePreserveTTLConditionally("value", func(_ interface{}, found bool) (interface{}, bool, bool, error) {
		if found {
			t.Fatal("原子删除后不应再命中")
		}
		return "created", false, false, nil
	}); err != nil {
		t.Fatalf("旧驱动的缺失项创建不应退化: %v", err)
	}
}
