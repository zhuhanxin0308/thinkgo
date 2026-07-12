package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultCacheConfigMatchesStrictDriverSchema 验证默认缓存配置只使用严格驱动支持的字段。
func TestDefaultCacheConfigMatchesStrictDriverSchema(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "config", "cache.json"))
	if err != nil {
		t.Fatalf("读取 cache.json 失败: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(content, &config); err != nil {
		t.Fatalf("cache.json 必须是合法 JSON: %v", err)
	}
	stores, ok := config["stores"].(map[string]interface{})
	if !ok {
		t.Fatal("cache.json 必须包含 stores")
	}
	redisStore, ok := stores["redis"].(map[string]interface{})
	if !ok {
		t.Fatal("cache.json 必须包含 redis store")
	}
	if prefix, ok := redisStore["prefix"].(string); !ok || prefix == "" {
		t.Fatalf("redis store 必须配置非空 prefix，实际为 %#v", redisStore["prefix"])
	}
	if _, exists := redisStore["timeout_ms"]; !exists {
		t.Fatalf("redis store 必须使用驱动读取的 timeout_ms 字段，实际配置为 %#v", redisStore)
	}
	if _, exists := redisStore["timeout"]; exists {
		t.Fatalf("redis store 不应继续使用 timeout 字段，实际配置为 %#v", redisStore)
	}
	if allow, ok := redisStore["allow_flush_db"].(bool); !ok || allow {
		t.Fatalf("redis store 必须显式保持空前缀清库授权关闭，实际为 %#v", redisStore["allow_flush_db"])
	}
	for name, rawStore := range stores {
		store, valid := rawStore.(map[string]interface{})
		if !valid {
			t.Fatalf("store %q 配置必须是对象", name)
		}
		if _, exists := store["expire"]; exists {
			t.Fatalf("store %q 不应保留未生效的 expire 字段", name)
		}
	}
}
