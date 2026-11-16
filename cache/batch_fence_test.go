package cache

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

type batchFenceProbe struct {
	*cacheDriver.Memory
	fenceReads [][]string
	single     int
	readErr    error
}

func (driver *batchFenceProbe) GetContext(_ context.Context, key string) (interface{}, bool, error) {
	if strings.HasPrefix(key, cacheFencePrefix) {
		driver.single++
	}
	return driver.Memory.Get(key)
}

func (driver *batchFenceProbe) GetManyStrictContext(_ context.Context, keys []string) (map[string]interface{}, error) {
	driver.fenceReads = append(driver.fenceReads, slices.Clone(keys))
	if driver.readErr != nil {
		return nil, driver.readErr
	}
	values := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		value, found, err := driver.Memory.Get(key)
		if err != nil {
			return nil, err
		}
		if found {
			values[key] = value
		}
	}
	return values, nil
}

func TestBatchReadsDistinctFenceKeysOnceAndRejectsStaleValues(t *testing.T) {
	ctx := t.Context()
	probe := &batchFenceProbe{Memory: cacheDriver.NewMemory()}
	driver, err := NewNamespaceDriver(probe, "tenant", "")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewCache(nil, driver)
	input := map[string]interface{}{"exact": "stale", "prefix:old": "stale", "visible": nil, "prefix:new": "new"}
	if err := manager.SetMany(input, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.beginKeyInvalidation(ctx, driver, []string{"exact"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.beginPrefixInvalidation(ctx, driver, "prefix:"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set("prefix:new", "updated", time.Minute); err != nil {
		t.Fatal(err)
	}
	probe.single = 0
	keys := []string{"exact", "prefix:old", "visible", "visible", "prefix:new"}
	values, err := manager.GetManyContext(ctx, keys)
	if value, found := values["visible"]; err != nil || len(values) != 2 || !found || value != nil || values["prefix:new"] != "updated" {
		t.Fatalf("批量读取泄露旧值或丢失有效命中: %#v，%v", values, err)
	}
	if probe.single != 0 || len(probe.fenceReads) != 1 {
		t.Fatalf("批量读取复查失效水位的次数不正确: 单键=%d，批次=%v", probe.single, probe.fenceReads)
	}
	fs := probe.fenceReads[0]
	want := map[string]bool{driver.key(manager.scopedInvalidationKey()): true, driver.key(manager.fenceInvalidationKey()): true}
	for _, key := range keys {
		want[driver.key(manager.keyInvalidationBucketKey(keyInvalidationBucket(key)))] = true
	}
	if len(fs) != len(want) || fs[len(fs)-1] != driver.key(manager.fenceInvalidationKey()) {
		t.Fatalf("应按桶去重并最后读取全局水位: %v", fs)
	}
	for _, key := range fs {
		if !want[key] {
			t.Errorf("收到未预期的失效水位键: %s", key)
		}
		delete(want, key)
	}
	probe.readErr = errors.New("失效水位读取失败")
	if values, err := manager.GetManyContext(ctx, keys); values != nil || !errors.Is(err, probe.readErr) {
		t.Fatalf("水位读失败时应拒绝返回缓存值: %#v，%v", values, err)
	}
}
