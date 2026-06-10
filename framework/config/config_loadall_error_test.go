package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigLoadAllReturnsLoadError 验证批量加载配置时不会吞掉损坏文件的错误。
func TestConfigLoadAllReturnsLoadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"app":`), 0o644); err != nil {
		t.Fatalf("写入损坏配置文件失败: %v", err)
	}

	err := NewConfig().LoadAll(dir)
	if err == nil {
		t.Fatal("损坏的配置文件应返回错误，而不是被静默忽略")
	}
	if !strings.Contains(err.Error(), "broken.json") {
		t.Fatalf("配置加载错误应包含文件名，实际为 %v", err)
	}
}
