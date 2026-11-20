package cache

import (
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// 同一套静态配置升级后，仍需通过命名空间驱动原有的物理标签索引清理数据。
func TestConfiguredTagPrefixReadsExistingNamespaceIndex(t *testing.T) {
	raw := driver.NewMemory()
	const configured = "catalog:"
	ns, err := NewNamespaceDriver(raw, "store:", configured)
	if err != nil {
		t.Fatal(err)
	}
	store := NewCache(nil, ns)
	const tag, key = "book", "book:1"
	if err := store.Set(key, "first"); err != nil {
		t.Fatal(err)
	}
	if err := ns.Set(tagMetaPrefix+cacheMetadataScope+":"+tag, []string{key}, 0); err != nil {
		t.Fatal(err)
	}
	if err := ns.Set(tagReverseKeyWithPrefix("", key), []string{tag}, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfigureStore(defaultStoreName, StoreOptions{TagPrefix: configured}); err != nil {
		t.Fatal(err)
	}
	view, err := store.Tag(tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ConfigureStore(defaultStoreName, StoreOptions{TagPrefix: defaultTagPrefix}); err != nil {
		t.Fatal(err)
	}
	if err := view.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(key); err != nil || !found {
		t.Fatalf("切换标签前缀后原索引应隔离：found=%t err=%v", found, err)
	}
	if err := store.ConfigureStore(defaultStoreName, StoreOptions{TagPrefix: configured}); err != nil {
		t.Fatal(err)
	}
	if err := view.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(key); err != nil || found {
		t.Fatalf("相同配置升级后旧标签应可失效：found=%t err=%v", found, err)
	}
}

// 标签按并集清理，一个孤立的正向成员不能食掉另一条有双向证明的边。
func TestFlushMixedValidAndOrphanTagEdges(t *testing.T) {
	raw := driver.NewMemory()
	store := NewCache(nil, raw)
	const key = "indexed"
	valid, err := store.Tag("valid")
	if err != nil {
		t.Fatal(err)
	}
	if err := valid.Set(key, "value"); err != nil {
		t.Fatal(err)
	}
	if err := raw.Set(store.tagMetaKey("orphan"), []string{key}, 0); err != nil {
		t.Fatal(err)
	}
	view, err := store.Tag("orphan", "valid")
	if err != nil {
		t.Fatal(err)
	}
	if err := view.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(key); err != nil || found {
		t.Fatalf("有双向证明的成员未失效：found=%t err=%v", found, err)
	}
	for _, tag := range []string{"orphan", "valid"} {
		if _, found, err := raw.Get(store.tagMetaKey(tag)); err != nil || found {
			t.Fatalf("标签索引 %q 未清理：found=%t err=%v", tag, found, err)
		}
	}
}
