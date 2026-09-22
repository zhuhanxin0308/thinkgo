package cache

import (
	"testing"
	"time"
)

type thinkPHPStoreDriver struct {
	key string
	ttl time.Duration
}

func (driver *thinkPHPStoreDriver) Get(string) (interface{}, bool, error) { return nil, false, nil }
func (driver *thinkPHPStoreDriver) Set(key string, _ interface{}, ttl time.Duration) error {
	driver.key = key
	driver.ttl = ttl
	return nil
}
func (driver *thinkPHPStoreDriver) Has(string) (bool, error)         { return false, nil }
func (driver *thinkPHPStoreDriver) Delete(string) error              { return nil }
func (driver *thinkPHPStoreDriver) Clear() error                     { return nil }
func (driver *thinkPHPStoreDriver) Inc(string, int64) (int64, error) { return 0, nil }
func (driver *thinkPHPStoreDriver) Dec(string, int64) (int64, error) { return 0, nil }

// TestThinkPHPStorePrefixAndDefaultExpire 验证省略 Set 有效期时使用 store.expire，
// 并把 store.prefix 真正应用到底层缓存键。
func TestThinkPHPStorePrefixAndDefaultExpire(t *testing.T) {
	backend := &thinkPHPStoreDriver{}
	namespaced, err := NewNamespaceDriver(backend, "tenant:", "tag:")
	if err != nil {
		t.Fatalf("创建命名空间驱动失败: %v", err)
	}
	manager := NewCache(nil, namespaced)
	if err := manager.ConfigureStore(defaultStoreName, StoreOptions{
		Expire:    30 * time.Second,
		TagPrefix: "tag:",
	}); err != nil {
		t.Fatalf("配置默认 store 失败: %v", err)
	}
	if err := manager.Set("order", "paid"); err != nil {
		t.Fatalf("写入默认缓存失败: %v", err)
	}
	if backend.key != "tenant:order" || backend.ttl != 30*time.Second {
		t.Fatalf("store 选项未生效: key=%q ttl=%s", backend.key, backend.ttl)
	}
	if err := manager.Set("forever", true, 0); err != nil {
		t.Fatalf("显式永久缓存失败: %v", err)
	}
	if backend.ttl != 0 {
		t.Fatalf("显式 0 必须覆盖默认有效期: %s", backend.ttl)
	}
}
