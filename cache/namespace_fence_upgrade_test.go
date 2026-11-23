package cache

import (
	"errors"
	"math"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// 忠实保留旧版命名空间只检查直接下层方法集的选择，其余读写走实际缓存管理器。
type legacyFenceNamespace struct{ *NamespaceDriver }

func (driver legacyFenceNamespace) usesFencing(cacheFenceOperation) bool {
	_, atomic := driver.driver.(AtomicUpdater)
	_, ttl := driver.driver.(TTLAtomicUpdater)
	return atomic && ttl
}

func TestPartialAtomicNamespaceKeepsFormatAtEachDepth(t *testing.T) {
	file, err := cacheDriver.NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var backend Driver = struct {
		Driver
		AtomicUpdater
	}{file, file}
	for depth := 0; depth <= 3; depth++ {
		manager := NewCache(nil, backend)
		if err := manager.Set("depth", "value", 0); err != nil {
			t.Fatal(err)
		}
		raw, found, err := backend.Get("depth")
		if err != nil || !found {
			t.Fatalf("第 %d 层缺少新写入的记录：%t %v", depth, found, err)
		}
		value, _, enveloped, err := decodeCacheValueEnvelope(raw)
		if err != nil || value != "value" || enveloped != (depth >= 2) {
			t.Fatalf("第 %d 层未沿用旧写入格式：%v %t %v", depth, value, enveloped, err)
		}
		if depth < 3 {
			backend, err = NewNamespaceDriver(backend, "tenant:", "")
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestNestedNamespaceRetainsFencedCounterAndBatchFormats(t *testing.T) {
	file, err := cacheDriver.NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		backend Driver
	}{
		{"ttl", struct {
			Driver
			TTLAtomicUpdater
		}{file, file}},
		{"conditional", struct {
			Driver
			ConditionalTTLAtomicUpdater
		}{file, file}},
	} {
		t.Run(test.name, func(t *testing.T) {
			driver := nestedCapabilityDriver(t, test.backend)
			before := NewCache(nil, legacyFenceNamespace{driver})
			after := NewCache(nil, driver)
			if got, err := before.Inc(test.name, 2); err != nil || got != 2 {
				t.Fatalf("旧版写入计数信封失败：%d %v", got, err)
			}
			if got, err := after.Dec(test.name, 1); err != nil || got != 1 {
				t.Fatalf("升级后无法按原格式扣减计数：%d %v", got, err)
			}
			if got, err := before.Inc(test.name, 1); err != nil || got != 2 {
				t.Fatalf("旧客户端未能继续使用新写入的计数：%d %v", got, err)
			}
		})
	}
	t.Run("batch", func(t *testing.T) {
		driver := nestedCapabilityDriver(t, struct {
			Driver
			ExpiryAtomicUpdater
		}{file, file})
		manager := NewCache(nil, driver)
		const exact = int64(math.MaxInt64)
		if err := manager.SetMany(map[string]interface{}{"batch": exact}); err != nil {
			t.Fatal(err)
		}
		if value, found, err := NewCache(nil, legacyFenceNamespace{driver}).Get("batch"); err != nil || !found || value != exact {
			t.Fatalf("升级后批量写入丢失原有整数编码：%v %t %v", value, found, err)
		}
	})
}

func TestNestedNamespaceGuardedWriterKeepsPreciseValues(t *testing.T) {
	file, err := cacheDriver.NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	driver := nestedCapabilityDriver(t, struct {
		Driver
		DistributedLocker
		LockRenewer
		LockGuardedUpdater
	}{file, file, file, file})
	manager := NewCache(nil, driver)
	const exact = int64(math.MaxInt64)
	if value, err := manager.RememberWithLock("guarded", 0, time.Minute, func() (interface{}, error) { return exact, nil }); err != nil || value != exact {
		t.Fatalf("租约写回失败：%v %v", value, err)
	}
	if value, found, err := NewCache(nil, legacyFenceNamespace{driver}).Get("guarded"); err != nil || !found || value != exact {
		t.Fatalf("旧版读取升级后的租约写回时丢失整数精度：%v %t %v", value, found, err)
	}
}

func TestNestedNamespaceRetainsPersistedInvalidations(t *testing.T) {
	for _, mode := range []string{"basic", "contextual"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			open := func() *NamespaceDriver {
				file, err := cacheDriver.NewFile(directory)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "contextual" {
					return nestedCapabilityDriver(t, struct {
						Driver
						ContextualAtomicUpdater
						DistributedLocker
					}{file, file, file})
				}
				return nestedCapabilityDriver(t, struct {
					Driver
					AtomicUpdater
					DistributedLocker
				}{file, file, file})
			}
			const envelopeKey, rawKey = "account:envelope", "account:raw"
			legacy := legacyFenceNamespace{open()}
			before := NewCache(nil, legacy)
			if err := before.Set(envelopeKey, "old", 0); err != nil {
				t.Fatal(err)
			}
			envelope, found, err := legacy.Get(envelopeKey)
			if err != nil || !found {
				t.Fatalf("读取旧版写入的信封失败：%v %t %v", envelope, found, err)
			}
			for _, key := range []string{envelopeKey, rawKey} {
				if err := before.Forget(key); err != nil {
					t.Fatal(err)
				}
			}
			// 模拟失效水位落盘之后才到达的物理写入，旧原始格式按零代处理。
			if err := legacy.Set(envelopeKey, envelope, 0); err != nil {
				t.Fatal(err)
			}
			if err := legacy.Set(rawKey, "old raw", 0); err != nil {
				t.Fatal(err)
			}
			if values, err := before.GetMany([]string{envelopeKey, rawKey}); err != nil || len(values) != 0 {
				t.Fatalf("升级前应已屏蔽两个旧值：%v %v", values, err)
			}
			after := NewCache(nil, open())
			for _, key := range []string{envelopeKey, rawKey} {
				if value, found, err := after.Get(key); err != nil || found {
					t.Errorf("升级后读取到失效值 %q：%v %t %v", key, value, found, err)
				}
				if found, err := after.Has(key); err != nil || found {
					t.Errorf("升级后仍报告失效键 %q 存在：%t %v", key, found, err)
				}
			}
			if values, err := after.GetMany([]string{envelopeKey, rawKey}); err != nil || len(values) != 0 {
				t.Errorf("升级后批量读取到失效值：%v %v", values, err)
			}
			if err := after.Update(envelopeKey, 0, func(value interface{}, found bool) (interface{}, bool, error) {
				if found {
					t.Errorf("更新回调读取到失效值：%v", value)
				}
				return "updated", false, nil
			}); err != nil {
				t.Fatal(err)
			}
			if value, err := after.Remember(rawKey, 0, func() (interface{}, error) { return "fresh", nil }); err != nil || value != "fresh" {
				t.Errorf("合并加载复用了失效值：%v %v", value, err)
			}
			for _, reader := range []*Cache{before, after} {
				values, err := reader.GetMany([]string{envelopeKey, rawKey})
				if err != nil || values[envelopeKey] != "updated" || values[rawKey] != "fresh" {
					t.Errorf("同一持久化空间的新值在新旧读方应均可见：%v %v", values, err)
				}
			}
			// 没有保留 TTL 或绝对过期时间的原子更新时，不能降级覆盖已有信封或水位。
			if _, err := after.Inc(envelopeKey, 1); !errors.Is(err, ErrCacheAtomicUpdateUnsupported) {
				t.Errorf("缺少保留 TTL 能力的计数未返回原错误：%v", err)
			}
			if err := after.SetMany(map[string]interface{}{rawKey: "batch"}); !errors.Is(err, ErrCacheAtomicUpdateUnsupported) {
				t.Errorf("缺少绝对过期时间能力的批量写入未返回原错误：%v", err)
			}
		})
	}
}
