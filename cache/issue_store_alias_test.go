package cache

import (
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

func TestStoreAliasesShareLocksWithoutLockingOtherStores(t *testing.T) {
	manager, named := newIssue148Aliases(t, cacheDriver.NewMemory())
	if err := manager.RegisterStore("other", cacheDriver.NewMemory()); err != nil {
		t.Fatal(err)
	}
	other, err := manager.Store("other")
	if err != nil {
		t.Fatal(err)
	}
	locks := make([]*Lock, 0, 3)
	for _, view := range []*Cache{manager, named, other} {
		lock, err := view.Lock("shared-job", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		locks = append(locks, lock)
		t.Cleanup(func() { _, _ = lock.Release() })
	}
	if acquired, err := locks[0].Acquire(); err != nil || !acquired {
		t.Fatalf("默认入口获取锁失败: acquired=%t err=%v", acquired, err)
	}
	if acquired, err := locks[1].Acquire(); err != nil || acquired {
		t.Fatalf("具名别名不得同时持有同一把锁: acquired=%t err=%v", acquired, err)
	}
	if acquired, err := locks[2].Acquire(); err != nil || !acquired {
		t.Fatalf("不同 store 的同名锁必须独立: acquired=%t err=%v", acquired, err)
	}
	if released, err := locks[0].Release(); err != nil || !released {
		t.Fatalf("默认入口释放锁失败: released=%t err=%v", released, err)
	}
	if acquired, err := locks[1].Acquire(); err != nil || !acquired {
		t.Fatalf("释放后具名别名应能获取同一把锁: acquired=%t err=%v", acquired, err)
	}
}

func TestFileStoreAliasIdentitySurvivesReversedRegistration(t *testing.T) {
	path := t.TempDir()
	open := func(names []string) *Cache {
		t.Helper()
		backend, err := cacheDriver.NewFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// File 是非默认 store；它的多个别名同样需要稳定的控制身份。
		manager := NewCache(nil, cacheDriver.NewMemory())
		t.Cleanup(func() { _ = manager.Close() })
		for _, name := range names {
			if err := manager.RegisterStore(name, backend); err != nil {
				t.Fatal(err)
			}
		}
		return manager
	}
	first := open([]string{"zeta", "alpha"})
	writer, err := first.Store("zeta")
	if err != nil {
		t.Fatal(err)
	}
	tagged, err := writer.Tag("persistent")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"first", "old"} {
		if err := tagged.Set("persisted", value, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := open([]string{"alpha", "zeta"})
	invalidator, err := second.Store("alpha")
	if err != nil {
		t.Fatal(err)
	}
	assertIssue148Value(t, invalidator, "persisted", "old", true)
	tagged, err = invalidator.Tag("persistent")
	if err != nil {
		t.Fatal(err)
	}
	if err := tagged.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "zeta"} {
		view, err := second.Store(name)
		if err != nil {
			t.Fatal(err)
		}
		assertIssue148Value(t, view, "persisted", nil, false)
	}
}

func TestAliasControlMetadataKeepsOtherNamespacesIsolated(t *testing.T) {
	backend := cacheDriver.NewMemory()
	left, err := NewNamespaceDriver(backend, "left:", "tag:")
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewNamespaceDriver(backend, "right:", "tag:")
	if err != nil {
		t.Fatal(err)
	}
	manager, alias := newIssue148Aliases(t, left)
	if err := manager.RegisterStore("other", right); err != nil {
		t.Fatal(err)
	}
	other, err := manager.Store("other")
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []*Cache{manager, other} {
		tagged, err := view.Tag("shared-tag")
		if err != nil {
			t.Fatal(err)
		}
		if err := tagged.Set("shared-key", "value", 0); err != nil {
			t.Fatal(err)
		}
	}
	tagged, err := alias.Tag("shared-tag")
	if err != nil {
		t.Fatal(err)
	}
	if err := tagged.Flush(); err != nil {
		t.Fatal(err)
	}
	assertIssue148Value(t, manager, "shared-key", nil, false)
	assertIssue148Value(t, other, "shared-key", "value", true)
	if err := alias.Set("shared-key", "new", 0); err != nil {
		t.Fatal(err)
	}
	if err := manager.Flush(); err != nil {
		t.Fatal(err)
	}
	assertIssue148Value(t, alias, "shared-key", nil, false)
	assertIssue148Value(t, other, "shared-key", "value", true)
	otherTag, err := other.Tag("shared-tag")
	if err != nil {
		t.Fatal(err)
	}
	if err := otherTag.Flush(); err != nil {
		t.Fatal(err)
	}
	assertIssue148Value(t, other, "shared-key", nil, false)
}

func TestStoreAliasesKeepPerViewDefaultTTL(t *testing.T) {
	backend := &thinkPHPStoreDriver{}
	manager, named := newIssue148Aliases(t, backend)
	if err := manager.ConfigureStore("default", StoreOptions{Expire: 30 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := manager.ConfigureStore("primary", StoreOptions{Expire: 90 * time.Second}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		view *Cache
		ttl  time.Duration
	}{{manager, 30 * time.Second}, {named, 90 * time.Second}, {manager, 30 * time.Second}} {
		if err := test.view.Set("key", "value"); err != nil {
			t.Fatal(err)
		}
		if backend.ttl != test.ttl {
			t.Fatalf("身份合并不得改写视图默认 TTL: got=%s want=%s", backend.ttl, test.ttl)
		}
	}
	if err := named.Set("key", "forever", 0); err != nil {
		t.Fatal(err)
	}
	if backend.ttl != 0 {
		t.Fatalf("显式永久有效期未覆盖视图默认值: %s", backend.ttl)
	}
}

type issue148NonComparableDriver struct {
	*cacheDriver.Memory
	options []string
}

func TestNonComparableValueDriverRegistrationRemainsUsable(t *testing.T) {
	backend := issue148NonComparableDriver{Memory: cacheDriver.NewMemory(), options: []string{"custom"}}
	manager, named := newIssue148Aliases(t, backend)
	if err := manager.Set("first", "default", 0); err != nil {
		t.Fatal(err)
	}
	assertIssue148Value(t, named, "first", "default", true)
	if err := named.Set("second", "named", 0); err != nil {
		t.Fatal(err)
	}
	assertIssue148Value(t, manager, "second", "named", true)
}

func newIssue148Aliases(t *testing.T, backend Driver) (*Cache, *Cache) {
	t.Helper()
	manager := NewCache(nil, backend)
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.RegisterStore("primary", backend); err != nil {
		t.Fatal(err)
	}
	named, err := manager.Store("primary")
	if err != nil {
		t.Fatal(err)
	}
	return manager, named
}

func assertIssue148Value(t *testing.T, view *Cache, key string, want interface{}, wantFound bool) {
	t.Helper()
	value, found, err := view.Get(key)
	if err != nil || found != wantFound || found && value != want {
		t.Fatalf("缓存可见性错误: key=%q value=%v found=%t err=%v; want=%v found=%t", key, value, found, err, want, wantFound)
	}
}

func awaitIssue148Signal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s 未到达确定性同步点", operation)
	}
}
