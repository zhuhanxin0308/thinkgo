package driver

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFileCacheRoundTripWithUnsafeKey 验证包含路径片段的缓存键也能安全读写，而不会逃逸缓存目录。
func TestFileCacheRoundTripWithUnsafeKey(t *testing.T) {
	basePath := t.TempDir()
	cacheDir := filepath.Join(basePath, "cache")
	driver := NewFile(cacheDir)

	protectedPath := filepath.Join(basePath, "protected.txt")
	if err := os.WriteFile(protectedPath, []byte("origin"), 0o600); err != nil {
		t.Fatalf("写入受保护文件失败: %v", err)
	}

	unsafeKey := "../protected.txt"
	driver.Set(unsafeKey, "cached-value", time.Minute)

	if value := driver.Get(unsafeKey); value != "cached-value" {
		t.Fatalf("缓存读写应保持一致，期望 cached-value，实际 %v", value)
	}

	content, err := os.ReadFile(protectedPath)
	if err != nil {
		t.Fatalf("读取受保护文件失败: %v", err)
	}
	if string(content) != "origin" {
		t.Fatalf("危险缓存键不应改写缓存目录外文件，实际内容 %q", string(content))
	}
}

// TestFileCacheDeleteWithUnsafeKey 验证危险缓存键不会删除缓存目录外的文件。
func TestFileCacheDeleteWithUnsafeKey(t *testing.T) {
	basePath := t.TempDir()
	cacheDir := filepath.Join(basePath, "cache")
	driver := NewFile(cacheDir)

	protectedPath := filepath.Join(basePath, "protected.txt")
	if err := os.WriteFile(protectedPath, []byte("origin"), 0o600); err != nil {
		t.Fatalf("写入受保护文件失败: %v", err)
	}

	driver.Delete("../protected.txt")

	if _, err := os.Stat(protectedPath); err != nil {
		t.Fatalf("危险缓存键不应删除缓存目录外文件: %v", err)
	}
}

// TestMemoryCacheRemovesExpiredEntry 验证内存缓存读取到过期项后会立即移除，避免长期堆积。
func TestMemoryCacheRemovesExpiredEntry(t *testing.T) {
	driver := NewMemory()
	driver.Set("expired", "value", time.Millisecond)

	time.Sleep(5 * time.Millisecond)

	if value := driver.Get("expired"); value != nil {
		t.Fatalf("过期缓存应返回 nil，实际为 %v", value)
	}
	if len(driver.items) != 0 {
		t.Fatalf("读取过期缓存后应立即清理 map 项，实际残留 %d 项", len(driver.items))
	}
}
