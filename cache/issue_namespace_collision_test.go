package cache

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

func TestIssue126NamespaceTagsDoNotAliasPublicKeys(t *testing.T) {
	for _, tagPrefix := range []string{"", "custom:", "data:reverse:", "标签:"} {
		t.Run(tagPrefix, func(t *testing.T) {
			backend := cacheDriver.NewMemory()
			namespaced, err := NewNamespaceDriver(backend, "single:", tagPrefix)
			if err != nil {
				t.Fatal(err)
			}
			manager := NewCache(nil, namespaced)
			t.Cleanup(func() { _ = manager.Close() })
			tagged, err := manager.Tag("billing")
			if err != nil {
				t.Fatal(err)
			}
			if err := tagged.Set("dummy-invoice", "invoice", time.Minute); err != nil {
				t.Fatal(err)
			}
			aliases := []string{
				namespaced.tagPrefix + "default:billing",
				namespaced.tagPrefix + "reverse:" + strings.TrimPrefix(manager.tagReverseKey("dummy-invoice"), tagReversePrefix),
			}
			for _, alias := range aliases {
				if value, found, err := manager.Get(alias); err != nil || found {
					t.Fatalf("public Get exposed tag metadata: key=%q value=%v found=%t err=%v", alias, value, found, err)
				}
				if found, err := manager.Has(alias); err != nil || found {
					t.Fatalf("public Has exposed tag metadata: key=%q found=%t err=%v", alias, found, err)
				}
				if err := manager.Set(alias, "public-value"); err != nil {
					t.Fatal(err)
				}
				if err := manager.Forget(alias); err != nil {
					t.Fatal(err)
				}
			}
			if members, found, err := backend.Get(namespaced.key(manager.tagMetaKey("billing"))); err != nil || !found || !reflect.DeepEqual(members, []string{"dummy-invoice"}) {
				t.Fatalf("public write/delete corrupted tag membership: %v %t %v", members, found, err)
			}
			if err := manager.Set(aliases[0], "public-value"); err != nil {
				t.Fatal(err)
			}
			if err := tagged.Flush(); err != nil {
				t.Fatal(err)
			}
			if _, found, err := manager.Get("dummy-invoice"); err != nil || found {
				t.Fatalf("tag invalidation lost its member: found=%t err=%v", found, err)
			}
			if value, found, err := manager.Get(aliases[0]); err != nil || !found || value != "public-value" {
				t.Fatalf("tag invalidation removed unrelated public value: %v %t %v", value, found, err)
			}
		})
	}
}

func TestIssue128NestedNamespacesKeepAllOperationsIsolated(t *testing.T) {
	for _, prefixes := range [][2]string{{"tenant:", "tenant:vip:"}, {"", "tenant:"}, {"租户:", "租户:管理员:"}, {"a:", "a:1:"}} {
		t.Run(prefixes[1], func(t *testing.T) {
			backend := cacheDriver.NewMemory()
			short, err := NewNamespaceDriver(backend, prefixes[0], "")
			if err != nil {
				t.Fatal(err)
			}
			long, err := NewNamespaceDriver(backend, prefixes[1], "")
			if err != nil {
				t.Fatal(err)
			}
			alias := strings.TrimPrefix(prefixes[1], prefixes[0]) + "card"
			if err := long.Set("card", int64(7), time.Minute); err != nil {
				t.Fatal(err)
			}
			if value, found, err := short.Get(alias); err != nil || found {
				t.Fatalf("nested prefix disclosed another namespace: %v %t %v", value, found, err)
			}
			if values, err := short.GetMany([]string{alias, "card"}); err != nil || len(values) != 0 {
				t.Fatalf("batch read crossed namespaces: %v %v", values, err)
			}
			if err := short.SetMany(map[string]interface{}{alias: int64(1)}, 0); err != nil {
				t.Fatal(err)
			}
			if value, err := short.Inc(alias, 1); err != nil || value != 2 {
				t.Fatalf("local counter changed another namespace: %d %v", value, err)
			}
			if err := short.Update(alias, 0, func(value interface{}, found bool) (interface{}, bool, error) {
				if !found || value != int64(2) {
					t.Fatalf("atomic read crossed namespaces: %v %t", value, found)
				}
				return int64(3), false, nil
			}); err != nil {
				t.Fatal(err)
			}
			if acquired, err := long.AcquireLock("card", "vip-owner", time.Minute); err != nil || !acquired {
				t.Fatalf("VIP lease: %t %v", acquired, err)
			}
			if acquired, err := short.AcquireLock(alias, "local-owner", time.Minute); err != nil || !acquired {
				t.Fatalf("nested namespace collided with another lease: %t %v", acquired, err)
			}
			operations := []struct {
				name string
				run  func() error
			}{
				{"delete", func() error { return short.Delete(alias) }},
				{"clear", short.Clear},
				{"context_clear", func() error { return short.ClearContext(context.Background()) }},
				{"prefix_clear", func() error { return short.ClearPrefix(strings.TrimSuffix(alias, "card")) }},
				{"conditional_clear", func() error {
					return short.ClearPrefixIfContext(context.Background(), "", nil, func(interface{}) (bool, error) { return true, nil })
				}},
			}
			for _, operation := range operations {
				if err := short.Set(alias, "local", 0); err != nil {
					t.Fatal(err)
				}
				if err := operation.run(); err != nil {
					t.Fatalf("%s: %v", operation.name, err)
				}
				if _, found, err := short.Get(alias); err != nil || found {
					t.Fatalf("%s did not clear its own key: %t %v", operation.name, found, err)
				}
				if value, found, err := long.Get("card"); err != nil || !found || value != int64(7) {
					t.Fatalf("%s changed another namespace: %v %t %v", operation.name, value, found, err)
				}
			}
		})
	}
}

func TestNamespaceMigrationDoesNotReadOrRemoveAmbiguousLegacyKeys(t *testing.T) {
	backend := cacheDriver.NewMemory()
	if err := backend.Set("tenant:vip:card", "legacy", 0); err != nil {
		t.Fatal(err)
	}
	namespaced, err := NewNamespaceDriver(backend, "tenant:", "")
	if err != nil {
		t.Fatal(err)
	}
	if value, found, err := namespaced.Get("vip:card"); err != nil || found {
		t.Fatalf("ambiguous legacy key was reused: %v %t %v", value, found, err)
	}
	if err := namespaced.Clear(); err != nil {
		t.Fatal(err)
	}
	if value, found, err := backend.Get("tenant:vip:card"); err != nil || !found || value != "legacy" {
		t.Fatalf("legacy key with unknown ownership was deleted: %v %t %v", value, found, err)
	}
}
