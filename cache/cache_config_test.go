package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultCacheConfigMatchesStrictDriverSchema 验证默认缓存配置保留 ThinkPHP
// file store 的完整公共字段，并由应用装配层真实应用这些字段。
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
	if len(stores) != 1 {
		t.Fatalf("ThinkPHP 默认只声明 file store，实际为 %#v", stores)
	}
	if _, ok := stores["file"].(map[string]interface{}); !ok {
		t.Fatal("cache.json 必须包含 file store")
	}
	store := stores["file"].(map[string]interface{})
	for _, field := range []string{"type", "path", "prefix", "expire", "tag_prefix", "serialize"} {
		if _, exists := store[field]; !exists {
			t.Fatalf("默认 file store 缺少 ThinkPHP 字段 %q", field)
		}
	}
}
