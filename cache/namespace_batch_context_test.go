package cache

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

type namespaceContextProbe struct {
	Driver
	readKeys   []string
	writeKeys  []string
	contexts   []context.Context
	cancelRead context.CancelFunc
	cancelSet  context.CancelFunc
}

func (probe *namespaceContextProbe) GetContext(ctx context.Context, key string) (interface{}, bool, error) {
	probe.readKeys = append(probe.readKeys, key)
	probe.contexts = append(probe.contexts, ctx)
	if probe.cancelRead != nil {
		probe.cancelRead()
	}
	return probe.Driver.Get(key)
}

func (probe *namespaceContextProbe) SetContext(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	probe.writeKeys = append(probe.writeKeys, key)
	probe.contexts = append(probe.contexts, ctx)
	if probe.cancelSet != nil {
		probe.cancelSet()
	}
	return probe.Driver.Set(key, value, ttl)
}

type namespaceSyncBatchProbe struct{ *namespaceContextProbe }

func (*namespaceSyncBatchProbe) GetMany([]string) (map[string]interface{}, error) {
	panic("Context 入口不能绕过逐键取消而调用同步批量接口")
}
func (*namespaceSyncBatchProbe) SetMany(map[string]interface{}, time.Duration) error {
	panic("Context 入口不能绕过逐键取消而调用同步批量接口")
}

// TestNamespaceBatchRejectsInvalidContext 验证直接调用时 nil 和已取消上下文不产生任何写入。
func TestNamespaceBatchRejectsInvalidContext(t *testing.T) {
	backend := cacheDriver.NewMemory()
	driver, err := NewNamespaceDriver(backend, "tenant:", "")
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, testCase := range []struct {
		ctx  context.Context
		want error
	}{{nil, ErrInvalidCacheContext}, {canceled, context.Canceled}} {
		if err := driver.SetManyContext(testCase.ctx, map[string]interface{}{"key": "value"}, 0); !errors.Is(err, testCase.want) {
			t.Fatalf("已失效写入上下文未被拒绝: %v", err)
		}
		if _, err := driver.GetManyContext(testCase.ctx, []string{"key"}); !errors.Is(err, testCase.want) {
			t.Fatalf("已失效读取上下文未被拒绝: %v", err)
		}
	}
	if _, found, err := backend.Get(driver.key("key")); err != nil || found {
		t.Fatal("取消后仍发生实际写入", err)
	}
}

// TestNamespaceNativeBatchRejectsInvalidContext 验证具备原生 Context 批量能力时也先完成上下文校验。
func TestNamespaceNativeBatchRejectsInvalidContext(t *testing.T) {
	backend := &contextTrackingCacheDriver{Memory: cacheDriver.NewMemory()}
	driver, err := NewNamespaceDriver(backend, "tenant:", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, testCase := range []struct {
		ctx  context.Context
		want error
	}{{nil, ErrInvalidCacheContext}, {ctx, context.Canceled}} {
		if _, err := driver.GetManyContext(testCase.ctx, []string{"key"}); !errors.Is(err, testCase.want) {
			t.Fatalf("原生批量读取未拒绝失效上下文: %v", err)
		}
		if err := driver.SetManyContext(testCase.ctx, map[string]interface{}{"key": 1}, 0); !errors.Is(err, testCase.want) {
			t.Fatalf("原生批量写入未拒绝失效上下文: %v", err)
		}
	}
	_, _, readContext, writeContext, _, _, _, _ := backend.contexts()
	if readContext != nil || writeContext != nil {
		t.Fatal("失效上下文仍触达底层原生批量接口")
	}
}

// TestNamespaceBatchFallbackCancellation 覆盖逐键 Context 能力、同步 Batch 能力及 Cache 门面。
func TestNamespaceBatchFallbackCancellation(t *testing.T) {
	for _, facade := range []bool{false, true} {
		for _, syncBatch := range []bool{false, true} {
			probe := &namespaceContextProbe{Driver: cacheDriver.NewMemory()}
			var backend Driver = probe
			if syncBatch {
				backend = &namespaceSyncBatchProbe{probe}
			}
			driver, err := NewNamespaceDriver(backend, "tenant:", "")
			if err != nil {
				t.Fatal(err)
			}
			get := driver.GetManyContext
			set := driver.SetManyContext
			if facade {
				cache := NewCache(nil, driver)
				get = cache.GetManyContext
				set = func(ctx context.Context, values map[string]interface{}, ttl time.Duration) error {
					return cache.SetManyContext(ctx, values, ttl)
				}
			}
			readContext, cancelRead := context.WithCancel(context.Background())
			probe.cancelRead = cancelRead
			if _, err := get(readContext, []string{"one", "two"}); !errors.Is(err, context.Canceled) || len(probe.readKeys) != 1 {
				t.Fatalf("第一项后取消未阻止继续读取: 次数=%d, 错误=%v", len(probe.readKeys), err)
			}
			if probe.readKeys[0] != driver.key("one") || probe.contexts[0] != readContext {
				t.Fatal("逐键读取丢失上下文或物理命名空间")
			}
			probe.cancelRead = nil
			writeContext, cancelSet := context.WithCancel(context.Background())
			probe.cancelSet = cancelSet
			if err := set(writeContext, map[string]interface{}{"one": 1, "two": 2}, time.Minute); !errors.Is(err, context.Canceled) || len(probe.writeKeys) != 1 {
				t.Fatalf("第一项后取消未阻止继续写入: 次数=%d, 错误=%v", len(probe.writeKeys), err)
			}
			probe.cancelSet = nil
			if err := set(context.Background(), map[string]interface{}{"one": 1, "two": 2}, 0); err != nil {
				t.Fatal(err)
			}
			values, err := get(context.Background(), []string{"one", "two", "missing"})
			if err != nil || !reflect.DeepEqual(values, map[string]interface{}{"one": 1, "two": 2}) {
				t.Fatalf("成功批次未恢复逻辑键: %v, %v", values, err)
			}
		}
	}
}
