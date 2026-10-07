package cache

import (
	"errors"
	"strings"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

func TestIssue39ConfigureStoreRejectsIgnoredPrefixAtomically(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	original := StoreOptions{Expire: time.Minute, TagPrefix: "original:"}
	if err := manager.ConfigureStore(defaultStoreName, original); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"tenant-a:", "tenant-b:", "租户:", " "} {
		t.Run(prefix, func(t *testing.T) {
			err := manager.ConfigureStore(defaultStoreName, StoreOptions{Prefix: prefix, Expire: time.Hour, TagPrefix: "changed:"})
			if !errors.Is(err, ErrInvalidCacheStore) || !strings.Contains(err.Error(), "NewNamespaceDriver") {
				t.Fatalf("ignored prefix must fail with actionable store error: %v", err)
			}
			manager.state.storesMu.RLock()
			got := manager.state.storeOptions[defaultStoreName]
			manager.state.storesMu.RUnlock()
			if got != original {
				t.Fatalf("failed configuration changed options: %#v", got)
			}
		})
	}
	if err := manager.ConfigureStore(defaultStoreName, StoreOptions{Expire: time.Second}); err != nil {
		t.Fatal(err)
	}
	manager.state.storesMu.RLock()
	got := manager.state.storeOptions[defaultStoreName]
	manager.state.storesMu.RUnlock()
	if got.Expire != time.Second || got.TagPrefix != "tag:" {
		t.Fatalf("valid options did not apply: %#v", got)
	}
}

func TestIssue39ExplicitNamespacesKeepSharedBackendIsolated(t *testing.T) {
	backend := cacheDriver.NewMemory()
	a, err := NewNamespaceDriver(backend, "tenant-a:", "tag:")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewNamespaceDriver(backend, "tenant-b:", "tag:")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewCache(nil, a)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := manager.RegisterStore("b", b); err != nil {
		t.Fatal(err)
	}
	other, err := manager.Store("b")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ConfigureStore(defaultStoreName, StoreOptions{Expire: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set("same", "A"); err != nil {
		t.Fatal(err)
	}
	if err := other.Set("same", "B"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		store *Cache
		want  string
	}{{manager, "A"}, {other, "B"}} {
		value, found, err := tc.store.Get("same")
		if err != nil || !found || value != tc.want {
			t.Fatalf("read crossed namespaces: %v %v %v", value, found, err)
		}
	}
	if err := manager.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := manager.Get("same"); err != nil || found {
		t.Fatalf("local flush failed: %v %v", found, err)
	}
	if value, found, err := other.Get("same"); err != nil || !found || value != "B" {
		t.Fatalf("flush crossed namespaces: %v %v %v", value, found, err)
	}
}

func TestIssue39NamespaceValidationStillRejectsInvalidValues(t *testing.T) {
	backend := cacheDriver.NewMemory()
	for _, tc := range []struct {
		name, prefix, tag string
		want              error
	}{
		{"prefix_control", "tenant\x00:", "tag:", ErrInvalidCacheKey},
		{"prefix_utf8", string([]byte{0xff}), "tag:", ErrInvalidCacheKey},
		{"prefix_size", strings.Repeat("x", maxCacheKeyBytes+1), "tag:", ErrInvalidCacheKey},
		{"tag_control", "tenant:", "tag\n", ErrInvalidCacheTag},
		{"tag_utf8", "tenant:", string([]byte{0xff}), ErrInvalidCacheTag},
		{"tag_size", "tenant:", strings.Repeat("x", maxCacheTagBytes+1), ErrInvalidCacheTag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewNamespaceDriver(backend, tc.prefix, tc.tag); !errors.Is(err, tc.want) {
				t.Fatalf("constructor validation: %v want %v", err, tc.want)
			}
		})
	}
	wrapper, err := NewNamespaceDriver(backend, "tenant:", "")
	if err != nil || wrapper.tagPrefix != "tag:" {
		t.Fatalf("default tag prefix: %v", err)
	}
	if _, err := NewNamespaceDriver(nil, "tenant:", "tag:"); !errors.Is(err, ErrCacheDriverNotConfigured) {
		t.Fatalf("nil driver accepted: %v", err)
	}
}
