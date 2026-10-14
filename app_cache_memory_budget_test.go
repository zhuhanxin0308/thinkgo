package framework

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

func memoryBudgetConfig() map[string]interface{} {
	return map[string]interface{}{"type": "memory", "max_entries": 16, "max_value_bytes": 512, "max_bytes": 8192, "max_locks": 8, "expire": 60}
}

func TestAppMemoryBudgetConfiguration(t *testing.T) {
	for _, value := range []interface{}{512, int64(512), json.Number("512"), float64(512)} {
		raw := memoryBudgetConfig()
		raw["max_value_bytes"] = value
		spec, err := parseAppCacheStore(func(path string) (string, error) { return path, nil }, "memory", raw)
		if err != nil || spec.memoryBudget == nil || spec.memoryBudget.MaxValueBytes != 512 {
			t.Fatal(spec, err)
		}
		backend, err := createAppCacheDriver(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := backend.Set("payload", strings.Repeat("x", 512), 0); !errors.Is(err, cacheDriver.ErrMemoryValueTooLarge) {
			t.Fatal("factory did not enforce budget", err)
		}
	}
	for _, mutation := range []func(map[string]interface{}){
		func(m map[string]interface{}) { delete(m, "max_entries") },
		func(m map[string]interface{}) { delete(m, "max_value_bytes") },
		func(m map[string]interface{}) { delete(m, "max_bytes") },
		func(m map[string]interface{}) { delete(m, "max_locks") },
		func(m map[string]interface{}) { m["max_entries"] = 0 },
		func(m map[string]interface{}) { m["max_value_bytes"] = 0 },
		func(m map[string]interface{}) { m["max_value_bytes"] = -1 },
		func(m map[string]interface{}) { m["max_value_bytes"] = 1.5 },
		func(m map[string]interface{}) { m["max_locks"] = true },
		func(m map[string]interface{}) { m["max_bytes"] = "8192" },
		func(m map[string]interface{}) { m["max_bytes"] = 511 },
	} {
		raw := memoryBudgetConfig()
		mutation(raw)
		if _, err := parseAppCacheStore(func(path string) (string, error) { return path, nil }, "memory", raw); !errors.Is(err, cacheDriver.ErrInvalidMemoryBudget) {
			t.Fatalf("accepted incomplete/invalid budget %v: %v", raw, err)
		}
	}
}

func TestAppMemoryBudgetCacheFacade(t *testing.T) {
	app := NewApp(t.TempDir())
	manager, err := createAppCache(app, map[string]interface{}{"default": "memory", "stores": map[string]interface{}{"memory": memoryBudgetConfig()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Set("value", "first"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set("value", strings.Repeat("x", 1024)); !errors.Is(err, cacheDriver.ErrMemoryValueTooLarge) {
		t.Fatal("facade accepted large payload", err)
	}
	// A failed facade Set may advance its fencing ticket and invalidate the old
	// value; either a miss or the old value is safe, never the rejected payload.
	got, found, err := manager.Get("value")
	if err != nil || found && got != "first" {
		t.Fatal(got, found, err)
	}
	if err := manager.Set("value", "replacement"); err != nil {
		t.Fatal(err)
	}
	got, found, err = manager.Get("value")
	if err != nil || !found || got != "replacement" {
		t.Fatal(got, found, err)
	}
	if err := manager.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := manager.Get("value"); err != nil || found {
		t.Fatal(found, err)
	}
}
