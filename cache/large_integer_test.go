package cache

import (
	"encoding/json"
	"errors"
	"testing"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// TestCacheJSONIntegerRoundTrip 验证内存与 JSON 持久化后端对显式 json.Number 使用相同精度合同。
func TestCacheJSONIntegerRoundTrip(t *testing.T) {
	backends := map[string]func(*testing.T) Driver{
		"memory": func(*testing.T) Driver { return cacheDriver.NewMemory() },
		"file": func(t *testing.T) Driver {
			backend, err := cacheDriver.NewFile(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return backend
		},
		"redis": atomicOrderRedis,
	}
	for name, backend := range backends {
		t.Run(name, func(t *testing.T) {
			manager := NewCache(nil, backend(t))
			for _, text := range []string{"9223372036854775807", "9223372036854775808", "18446744073709551615", "-9223372036854775809", "123456789012345678901234567890"} {
				value := json.Number(text)
				if err := manager.Set("number", value); err != nil {
					t.Fatal(err)
				}
				got, found, err := manager.Get("number")
				if err != nil || !found || got != value {
					t.Errorf("整数往返失真: input=%s got=%#v found=%v err=%v", text, got, found, err)
				}
			}
			for _, invalid := range []json.Number{"", "invalid", "01", "+1", "NaN", "1e"} {
				if err := manager.Set("invalid", invalid); !errors.Is(err, ErrInvalidCacheValue) {
					t.Errorf("非法数字未在写入时拒绝: %q", invalid)
				}
			}
			if err := manager.Set("decimal", json.Number("1.5")); err != nil {
				t.Fatal(err)
			}
			decimal, found, err := manager.Get("decimal")
			var expectedDecimal interface{} = 1.5
			if name == "memory" {
				expectedDecimal = json.Number("1.5")
			}
			if err != nil || !found || decimal != expectedDecimal {
				t.Fatalf("既有小数类型改变: value=%#v err=%v", decimal, err)
			}
			if err := manager.Set("nil", nil); err != nil {
				t.Fatal(err)
			}
			if value, found, err := manager.Get("nil"); err != nil || !found || value != nil {
				t.Fatalf("nil 命中语义改变: value=%v found=%v err=%v", value, found, err)
			}
			if err := manager.SetMany(map[string]interface{}{"first": "valid", "last": json.Number("bad")}); !errors.Is(err, ErrInvalidCacheValue) {
				t.Fatalf("批量非法数字未在提交前拒绝: %v", err)
			}
			if _, found, err := manager.Get("first"); err != nil || found {
				t.Fatalf("可预见编码失败仍提交了批量前缀: found=%v err=%v", found, err)
			}
		})
	}
}

// TestCacheWideIntegerEnvelopeRejectsCorruption 验证扩展编码不接受非法文本或非整数。
func TestCacheWideIntegerEnvelopeRejectsCorruption(t *testing.T) {
	for _, text := range []string{"", "01", "+1", "1.5", "1e3", "null"} {
		if _, err := decodePreciseCacheInteger(text, cacheIntegerEncoding+"json-number-wide"); !errors.Is(err, ErrCorruptCacheEnvelope) {
			t.Fatalf("扩展整数信封接受损坏值 %q: %v", text, err)
		}
	}
}
