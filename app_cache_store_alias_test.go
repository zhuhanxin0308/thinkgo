package framework

import (
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
)

func TestAppCacheDefaultAliasSharesInvalidation(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		for _, direction := range []string{"default-to-named", "named-to-default"} {
			for _, operation := range []string{"forget", "prefix", "flush", "tag"} {
				t.Run(kind+"/"+direction+"/"+operation, func(t *testing.T) {
					manager := newIssue148AppCache(t, &App{BasePath: t.TempDir()}, kind)
					named, err := manager.Store("primary")
					if err != nil {
						t.Fatal(err)
					}
					other, err := manager.Store("other")
					if err != nil {
						t.Fatal(err)
					}
					writer, invalidator := manager, named
					if direction == "named-to-default" {
						writer, invalidator = named, manager
					}
					tagged, err := writer.Tag("catalog")
					if err != nil {
						t.Fatal(err)
					}
					// 多次写入使另一别名的独立起始序列不能误打误撞覆盖本记录。
					for _, value := range []string{"first", "old"} {
						if err := tagged.Set("item:one", value, 0); err != nil {
							t.Fatal(err)
						}
					}
					if err := writer.Set("unrelated", "keep", 0); err != nil {
						t.Fatal(err)
					}
					otherTagged, err := other.Tag("catalog")
					if err != nil {
						t.Fatal(err)
					}
					if err := otherTagged.Set("item:one", "isolated", 0); err != nil {
						t.Fatal(err)
					}
					assertIssue148AppValue(t, invalidator, "item:one", "old", true)
					switch operation {
					case "forget":
						err = invalidator.Forget("item:one")
					case "prefix":
						err = invalidator.ClearPrefix("item:")
					case "flush":
						err = invalidator.Flush()
					case "tag":
						var view *cache.TaggedCache
						view, err = invalidator.Tag("catalog")
						if err == nil {
							err = view.Flush()
						}
					}
					if err != nil {
						t.Fatalf("跨别名失效失败: %v", err)
					}
					for _, view := range []*cache.Cache{manager, named} {
						assertIssue148AppValue(t, view, "item:one", nil, false)
						assertIssue148AppValue(t, view, "unrelated", "keep", operation != "flush")
					}
					assertIssue148AppValue(t, other, "item:one", "isolated", true)
					if err := invalidator.Set("item:one", "new", 0); err != nil {
						t.Fatalf("失效后从另一入口写入失败: %v", err)
					}
					assertIssue148AppValue(t, writer, "item:one", "new", true)
					if err := tagged.Flush(); err != nil {
						t.Fatal(err)
					}
					for _, view := range []*cache.Cache{manager, named} {
						assertIssue148AppValue(t, view, "item:one", "new", true)
					}
					// 旧标签的反向关系必须解除，其他 store 的同名标签则仍可独立清理。
					if err := otherTagged.Flush(); err != nil {
						t.Fatal(err)
					}
					assertIssue148AppValue(t, other, "item:one", nil, false)
					assertIssue148AppValue(t, writer, "item:one", "new", true)
				})
			}
		}
	}
}

func TestAppFileCacheAliasMetadataSurvivesFactoryRecreation(t *testing.T) {
	for _, direction := range []string{"default-to-named", "named-to-default"} {
		t.Run(direction, func(t *testing.T) {
			app := &App{BasePath: t.TempDir()}
			first := newIssue148AppCache(t, app, "file")
			writer := first
			var err error
			if direction == "named-to-default" {
				writer, err = first.Store("primary")
				if err != nil {
					t.Fatal(err)
				}
			}
			tagged, err := writer.Tag("persisted")
			if err != nil {
				t.Fatal(err)
			}
			if err := tagged.Set("persisted:key", "old", 0); err != nil {
				t.Fatal(err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			second := newIssue148AppCache(t, app, "file")
			invalidator := second
			if direction == "default-to-named" {
				invalidator, err = second.Store("primary")
				if err != nil {
					t.Fatal(err)
				}
			}
			assertIssue148AppValue(t, invalidator, "persisted:key", "old", true)
			reopenedTag, err := invalidator.Tag("persisted")
			if err != nil {
				t.Fatal(err)
			}
			if err := reopenedTag.Flush(); err != nil {
				t.Fatalf("重建工厂后跨别名清理标签失败: %v", err)
			}
			assertIssue148AppValue(t, second, "persisted:key", nil, false)
			if err := invalidator.Set("persisted:key", "fresh", 0); err != nil {
				t.Fatal(err)
			}
			assertIssue148AppValue(t, second, "persisted:key", "fresh", true)
		})
	}
}

func newIssue148AppCache(t *testing.T, app *App, kind string) *cache.Cache {
	t.Helper()
	stores := make(map[string]interface{})
	for _, name := range []string{"primary", "other"} {
		store := map[string]interface{}{"type": kind, "prefix": "租户:", "tag_prefix": "标签:"}
		if kind == "file" {
			store["path"] = "runtime/cache-" + name
		}
		stores[name] = store
	}
	manager, err := createAppCache(app, map[string]interface{}{
		"default": "primary",
		"stores":  stores,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("关闭缓存工厂失败: %v", err)
		}
	})
	return manager
}

func assertIssue148AppValue(t *testing.T, view *cache.Cache, key string, want interface{}, wantFound bool) {
	t.Helper()
	value, found, err := view.Get(key)
	if err != nil || found != wantFound || found && value != want {
		t.Fatalf("缓存可见性错误: key=%q value=%v found=%t err=%v; want=%v found=%t", key, value, found, err, want, wantFound)
	}
}
