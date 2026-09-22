package cache

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

type legacyBatchDriver struct {
	*cacheDriver.Memory
}

func (driver *legacyBatchDriver) GetMany(keys []string) (map[string]interface{}, error) {
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

func (driver *legacyBatchDriver) SetMany(values map[string]interface{}, ttl time.Duration) error {
	for key, value := range values {
		if err := driver.Memory.Set(key, value, ttl); err != nil {
			return err
		}
	}
	return nil
}

// TestCacheBatchAPIFallbackAndNilHit 验证没有批量能力的驱动仍保持批量 API 的正确语义。
func TestCacheBatchAPIFallbackAndNilHit(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	if err := manager.SetMany(map[string]interface{}{
		"profile": "Ada",
		"nil":     nil,
	}, time.Minute); err != nil {
		t.Fatalf("缓存批量写入失败: %v", err)
	}
	values, err := manager.GetMany([]string{"profile", "missing", "nil", "profile"})
	if err != nil {
		t.Fatalf("缓存批量读取失败: %v", err)
	}
	if values["profile"] != "Ada" {
		t.Fatalf("缓存批量读取 profile 错误: %#v", values)
	}
	if value, exists := values["nil"]; !exists || value != nil {
		t.Fatalf("缓存批量读取 nil 命中错误: value=%#v exists=%t", value, exists)
	}
	if _, exists := values["missing"]; exists || len(values) != 2 {
		t.Fatalf("缓存批量读取未命中语义错误: %#v", values)
	}
}

// TestCacheBatchRejectsInvalidKeyBeforeWrite 验证批量参数校验失败时不会先写入其它键。
func TestCacheBatchRejectsInvalidKeyBeforeWrite(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	if err := manager.SetMany(map[string]interface{}{
		"valid": "value",
		"":      "invalid",
	}, time.Minute); err == nil {
		t.Fatal("缓存批量写入非法键必须失败")
	}
	if _, found, err := manager.Get("valid"); err != nil || found {
		t.Fatalf("批量参数校验失败后不应残留有效键: found=%t err=%v", found, err)
	}
}

// TestCacheBatchRejectsUnboundedInput 验证批量 API 在进入驱动前拒绝超大键集合。
func TestCacheBatchRejectsUnboundedInput(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	keys := make([]string, maxCacheBatchEntries+1)
	values := make(map[string]interface{}, len(keys))
	for index := range keys {
		keys[index] = "key-" + formatBatchIndex(index)
		values[keys[index]] = index
	}
	if _, err := manager.GetMany(keys); !errors.Is(err, ErrCacheBatchTooLarge) {
		t.Fatalf("超大批量读取应返回 ErrCacheBatchTooLarge，实际为 %v", err)
	}
	if err := manager.SetMany(values, time.Minute); !errors.Is(err, ErrCacheBatchTooLarge) {
		t.Fatalf("超大批量写入应返回 ErrCacheBatchTooLarge，实际为 %v", err)
	}
}

// TestCacheBatchContextSupportsFallbackAndLegacyDrivers 验证批量上下文覆盖逐键回退与旧批量接口。
func TestCacheBatchContextSupportsFallbackAndLegacyDrivers(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{ name string }{}, "batch")
	for name, manager := range map[string]*Cache{
		"逐键回退":  NewCache(nil, cacheDriver.NewMemory()),
		"旧批量接口": NewCache(nil, &legacyBatchDriver{Memory: cacheDriver.NewMemory()}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := manager.SetManyContext(ctx, map[string]interface{}{"one": 1, "two": 2}, time.Minute); err != nil {
				t.Fatalf("SetManyContext 失败: %v", err)
			}
			values, err := manager.GetManyContext(ctx, []string{"one", "two", "missing"})
			if err != nil || values["one"] != 1 || values["two"] != 2 || values["missing"] != nil {
				t.Fatalf("GetManyContext 结果错误: values=%#v err=%v", values, err)
			}
		})
	}
}

// TestCacheBatchContextRejectsCancellation 验证批量操作在进入驱动前响应请求取消。
func TestCacheBatchContextRejectsCancellation(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.GetManyContext(ctx, []string{"key"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 GetManyContext 应返回 context.Canceled，实际为 %v", err)
	}
	if err := manager.SetManyContext(ctx, map[string]interface{}{"key": "value"}, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 SetManyContext 应返回 context.Canceled，实际为 %v", err)
	}
}

func formatBatchIndex(index int) string {
	return strconv.Itoa(index)
}
