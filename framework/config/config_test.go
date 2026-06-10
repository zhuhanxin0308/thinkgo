package config

import (
	"reflect"
	"testing"
)

// TestConfigGetCachesResolvedDotLookup 验证点路径查询会写入缓存，避免每次重复拆分和逐层遍历。
func TestConfigGetCachesResolvedDotLookup(t *testing.T) {
	cfg := NewConfig()
	cfg.Set("app.server.port", 8080)

	if got := cfg.Get("app.server.port"); got != 8080 {
		t.Fatalf("点路径配置读取结果错误，期望 8080，实际为 %#v", got)
	}

	cacheField := reflect.ValueOf(cfg).Elem().FieldByName("lookupCache")
	if !cacheField.IsValid() {
		t.Fatal("Config 应维护点路径查询缓存")
	}
	if cacheField.Len() != 1 {
		t.Fatalf("首次点路径查询后应写入 1 条缓存，实际为 %d", cacheField.Len())
	}
	if !cacheField.MapIndex(reflect.ValueOf("app.server.port")).IsValid() {
		t.Fatal("点路径查询后应缓存 app.server.port 的解析结果")
	}
}

// TestConfigSetInvalidatesLookupCache 验证配置更新后会清空旧缓存，避免继续返回陈旧值。
func TestConfigSetInvalidatesLookupCache(t *testing.T) {
	cfg := NewConfig()
	cfg.Set("app.server.port", 8080)
	_ = cfg.Get("app.server.port")

	cfg.Set("app.server.port", 9090)

	cacheField := reflect.ValueOf(cfg).Elem().FieldByName("lookupCache")
	if !cacheField.IsValid() {
		t.Fatal("Config 应维护点路径查询缓存")
	}
	if cacheField.Len() != 0 {
		t.Fatalf("Set 后应清空旧查询缓存，实际残留 %d 条", cacheField.Len())
	}

	if got := cfg.Get("app.server.port"); got != 9090 {
		t.Fatalf("配置更新后应返回新值 9090，实际为 %#v", got)
	}
}
