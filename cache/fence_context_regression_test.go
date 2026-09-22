package cache

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver"
)

type fenceContextBackend struct {
	*cacheDriver.Memory
	received     context.Context
	key          string
	legacyCalled bool
}

func (backend *fenceContextBackend) Inc(string, int64) (int64, error) {
	backend.legacyCalled = true
	return 0, errors.New("不应调用无上下文的计数接口")
}

func (backend *fenceContextBackend) IncContext(ctx context.Context, key string, _ int64) (int64, error) {
	backend.received, backend.key = ctx, key
	return 0, context.DeadlineExceeded
}

// TestFenceAllocationPreservesRequestContext 验证公开写入及失效 API 均将请求期限传到命名空间内的代际分配。
func TestFenceAllocationPreservesRequestContext(t *testing.T) {
	operations := map[string]func(*Cache, context.Context) error{
		"set": func(cache *Cache, ctx context.Context) error {
			return cache.SetContext(ctx, "value", "new", time.Minute)
		},
		"batch": func(cache *Cache, ctx context.Context) error {
			return cache.SetManyContext(ctx, map[string]interface{}{"value": "new"}, time.Minute)
		},
		"update": func(cache *Cache, ctx context.Context) error {
			return cache.UpdateContext(ctx, "value", time.Minute, func(interface{}, bool) (interface{}, bool, error) { return "new", false, nil })
		},
		"flush":  func(cache *Cache, ctx context.Context) error { return cache.FlushContext(ctx) },
		"prefix": func(cache *Cache, ctx context.Context) error { return cache.ClearPrefixContext(ctx, "value") },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			backend := &fenceContextBackend{Memory: cacheDriver.NewMemory()}
			namespace, err := NewNamespaceDriver(backend, "tenant:", "")
			if err != nil {
				t.Fatal(err)
			}
			manager := NewCache(nil, namespace)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			if err := operation(manager, ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("期限错误丢失: %v", err)
			}
			if backend.received != ctx || backend.legacyCalled || !strings.Contains(backend.key, "namespace:") {
				t.Fatalf("代际上下文或命名空间丢失: legacy=%t key=%q", backend.legacyCalled, backend.key)
			}
			if _, found, err := manager.Get("value"); err != nil || found {
				t.Fatalf("领票失败后仍写入: %t %v", found, err)
			}
		})
	}
}
