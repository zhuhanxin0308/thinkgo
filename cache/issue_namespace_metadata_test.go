package cache

import (
	"errors"
	"reflect"
	"testing"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

func TestIssue141TagFlushSurvivesNamespaceCapacityPressure(t *testing.T) {
	forEachIssue141MemoryNamespace(t, 6, func(t *testing.T, driver Driver, manager *Cache) {
		tagged, err := manager.Tag("catalog")
		if err != nil {
			t.Fatal(err)
		}
		if err := tagged.Set("old", "old-value", 0); err != nil {
			t.Fatal(err)
		}
		if err := manager.Set("victim", "ordinary-value", 0); err != nil {
			t.Fatal(err)
		}
		if err := tagged.Set("live", "live-value", 0); err != nil {
			t.Fatal(err)
		}

		// The six counted entries are three tag records and three business values.
		// FIFO pressure must remove old and victim, leaving live for Flush to delete.
		// Do not read old through Cache before Flush: a miss would repair its tags.
		for _, key := range []string{"pressure-one", "pressure-two"} {
			if err := manager.Set(key, key, 0); err != nil {
				t.Fatal(err)
			}
		}
		if value, found, err := manager.Get("live"); err != nil || !found || value != "live-value" {
			t.Fatalf("capacity fixture lost the live tagged member: value=%v found=%t err=%v", value, found, err)
		}
		if err := tagged.Flush(); err != nil {
			t.Fatalf("tag Flush after FIFO pressure failed: %v", err)
		}
		if value, found, err := manager.Get("live"); err != nil || found {
			t.Fatalf("successful tag Flush left its live member: value=%v found=%t err=%v", value, found, err)
		}
		for _, key := range []string{"pressure-one", "pressure-two"} {
			if value, found, err := manager.Get(key); err != nil || !found || value != key {
				t.Fatalf("tag Flush changed unrelated business data: key=%q value=%v found=%t err=%v", key, value, found, err)
			}
		}
		for _, key := range []string{manager.tagMetaKey("catalog"), manager.tagReverseKey("old"), manager.tagReverseKey("live")} {
			if value, found, err := driver.Get(key); err != nil || found {
				t.Fatalf("tag Flush left stale metadata: key=%q value=%v found=%t err=%v", key, value, found, err)
			}
		}
	})
}

func TestIssue141NamespaceMetadataCannotBeEvictedAtCapacity(t *testing.T) {
	forEachIssue141MemoryNamespace(t, 2, func(t *testing.T, driver Driver, manager *Cache) {
		metadata := []struct {
			key    string
			values []string
		}{
			{manager.tagMetaKey("catalog"), []string{"member"}},
			{manager.tagReverseKey("member"), []string{"catalog"}},
		}
		for _, entry := range metadata {
			if err := driver.Set(entry.key, entry.values, 0); err != nil {
				t.Fatal(err)
			}
		}
		if err := manager.Set("ordinary", "value", 0); !errors.Is(err, cacheDriver.ErrMemoryCapacityExhausted) {
			t.Fatalf("full protected metadata must reject a business write: %v", err)
		}
		for _, entry := range metadata {
			if value, found, err := driver.Get(entry.key); err != nil || !found || !reflect.DeepEqual(value, entry.values) {
				t.Fatalf("capacity pressure changed protected metadata: key=%q value=%v found=%t err=%v", entry.key, value, found, err)
			}
		}
		if value, found, err := manager.Get("ordinary"); err != nil || found {
			t.Fatalf("rejected business write became visible: value=%v found=%t err=%v", value, found, err)
		}
	})
}

func TestIssue141NamespacedTaggedSetRollsBackCapacityFailure(t *testing.T) {
	forEachIssue141MemoryNamespace(t, 2, func(t *testing.T, driver Driver, manager *Cache) {
		tagged, err := manager.Tag("catalog")
		if err != nil {
			t.Fatal(err)
		}
		// Forward and reverse metadata fill both slots. The business value cannot
		// evict either record, so the failed tagged write must roll back both.
		if err := tagged.Set("member", "value", 0); !errors.Is(err, cacheDriver.ErrMemoryCapacityExhausted) {
			t.Fatalf("tagged write without a business slot must fail: %v", err)
		}
		for _, key := range []string{"member", manager.tagMetaKey("catalog"), manager.tagReverseKey("member")} {
			if value, found, err := driver.Get(key); err != nil || found {
				t.Fatalf("failed tagged write left partial state: key=%q value=%v found=%t err=%v", key, value, found, err)
			}
		}
		if err := manager.Set("after-failure", "recovered", 0); err != nil {
			t.Fatalf("tag rollback did not restore capacity: %v", err)
		}
		if value, found, err := manager.Get("after-failure"); err != nil || !found || value != "recovered" {
			t.Fatalf("write after rollback failed: value=%v found=%t err=%v", value, found, err)
		}
	})
}

func forEachIssue141MemoryNamespace(t *testing.T, capacity int, check func(*testing.T, Driver, *Cache)) {
	t.Helper()
	for _, test := range []struct {
		name       string
		namespaced bool
		prefix     string
		tagPrefix  string
		nested     bool
	}{
		{name: "unwrapped"},
		{name: "empty-prefix", namespaced: true},
		{name: "prefix", namespaced: true, prefix: "tenant:"},
		{name: "custom-tag-prefix", namespaced: true, prefix: "tenant:", tagPrefix: "custom:reverse:"},
		{name: "unicode", namespaced: true, prefix: "租户:", tagPrefix: "标签:"},
		{name: "nested-wrappers", namespaced: true, prefix: "tenant:", tagPrefix: "tags:", nested: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend, err := cacheDriver.NewMemoryWithMaxEntries(capacity)
			if err != nil {
				t.Fatal(err)
			}
			var driver Driver = backend
			if test.nested {
				driver, err = NewNamespaceDriver(driver, "outer:", "outer-tags:")
				if err != nil {
					t.Fatal(err)
				}
			}
			if test.namespaced {
				driver, err = NewNamespaceDriver(driver, test.prefix, test.tagPrefix)
				if err != nil {
					t.Fatal(err)
				}
			}
			manager := NewCache(nil, driver)
			t.Cleanup(func() {
				if err := manager.Close(); err != nil {
					t.Errorf("close cache: %v", err)
				}
			})
			check(t, driver, manager)
		})
	}
}
