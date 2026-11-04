package cache

import (
	"errors"
	"testing"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

func TestIssue141PhysicalNamespaceKeysAreReservedAtPublicEntrypoints(t *testing.T) {
	operations := map[string]func(*Cache, string) error{
		"set": func(manager *Cache, key string) error {
			return manager.Set(key, "replacement")
		},
		"get": func(manager *Cache, key string) error {
			_, _, err := manager.Get(key)
			return err
		},
		"has": func(manager *Cache, key string) error {
			_, err := manager.Has(key)
			return err
		},
		"forget": func(manager *Cache, key string) error {
			return manager.Forget(key)
		},
		"set-many": func(manager *Cache, key string) error {
			return manager.SetMany(map[string]interface{}{"valid-key": "partial", key: "replacement"})
		},
		"tagged-set": func(manager *Cache, key string) error {
			tagged, err := manager.Tag("users")
			if err != nil {
				return err
			}
			return tagged.Set(key, "replacement")
		},
	}
	for _, mode := range []string{"raw", "namespaced"} {
		for name, operation := range operations {
			t.Run(mode+"/"+name, func(t *testing.T) {
				backend := cacheDriver.NewMemory()
				var view Driver = backend
				const key = "__thinkgo_namespace__:v2:0::__thinkgo_tag__:4:tag::default:users"
				physical := key
				if mode == "namespaced" {
					namespace, err := NewNamespaceDriver(backend, "tenant:", "custom:")
					if err != nil {
						t.Fatal(err)
					}
					view = namespace
					physical = namespace.key(key)
				}
				if err := backend.Set(physical, "internal", 0); err != nil {
					t.Fatal(err)
				}
				manager := NewCache(nil, view)
				t.Cleanup(func() { _ = manager.Close() })
				if err := operation(manager, key); !errors.Is(err, ErrReservedCacheKey) {
					t.Fatalf("public operation accepted a physical key: %v", err)
				}
				if value, found, err := backend.Get(physical); err != nil || !found || value != "internal" {
					t.Fatalf("rejected operation changed backend state: value=%v found=%t err=%v", value, found, err)
				}
				if found, err := manager.Has("valid-key"); err != nil || found {
					t.Fatalf("invalid batch partially wrote a valid key: found=%t err=%v", found, err)
				}
			})
		}
	}
}

func TestIssue141NamespaceTextInsideBusinessKeysRemainsUsable(t *testing.T) {
	for _, mode := range []string{"raw", "namespaced"} {
		t.Run(mode, func(t *testing.T) {
			backend := cacheDriver.NewMemory()
			var view Driver = backend
			if mode == "namespaced" {
				namespace, err := NewNamespaceDriver(backend, "租户:", "标签:")
				if err != nil {
					t.Fatal(err)
				}
				view = namespace
			}
			manager := NewCache(nil, view)
			t.Cleanup(func() { _ = manager.Close() })
			const key = "app:__thinkgo_namespace__:v2:0::__thinkgo_tag__:4:tag::default:users"
			if err := manager.Set(key, "value"); err != nil {
				t.Fatal(err)
			}
			if value, found, err := manager.Get(key); err != nil || !found || value != "value" {
				t.Fatalf("embedded namespace text was rejected: value=%v found=%t err=%v", value, found, err)
			}
			if err := manager.Forget(key); err != nil {
				t.Fatal(err)
			}
			if found, err := manager.Has(key); err != nil || found {
				t.Fatalf("Forget failed for ordinary key: found=%t err=%v", found, err)
			}
			tagged, err := manager.Tag("users")
			if err != nil {
				t.Fatal(err)
			}
			if err := tagged.Set(key, "tagged"); err != nil {
				t.Fatal(err)
			}
			if err := tagged.Flush(); err != nil {
				t.Fatal(err)
			}
			if found, err := manager.Has(key); err != nil || found {
				t.Fatalf("tag invalidation failed for ordinary key: found=%t err=%v", found, err)
			}
		})
	}
}
