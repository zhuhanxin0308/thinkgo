package framework

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInitializeOnlyLoadsRootConfigDirectory 验证单应用配置只来自根 config，
// app/config 不会作为多应用覆盖层被隐式读取。
func TestInitializeOnlyLoadsRootConfigDirectory(t *testing.T) {
	basePath := t.TempDir()
	rootConfigPath := filepath.Join(basePath, "config")
	applicationConfigPath := filepath.Join(basePath, "app", "config")
	for _, directory := range []string{rootConfigPath, applicationConfigPath} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("创建配置目录失败: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(rootConfigPath, "app.json"), []byte(`{"app_debug":false,"app_name":"root"}`), 0o600); err != nil {
		t.Fatalf("写入项目配置失败: %v", err)
	}
	requiredConfig := map[string]string{
		"cache.json":      `{"default":"file","stores":{"file":{"type":"File","path":"","prefix":"","expire":0,"tag_prefix":"tag:","serialize":[]}}}`,
		"filesystem.json": `{"default":"local","disks":{"local":{"type":"local","root":"./runtime/storage"}}}`,
		"log.json":        `{"default":"file","level":[],"type_channel":{},"close":false,"channels":{"file":{"type":"File","path":"","single":false,"apart_level":[],"max_files":0,"json":false,"close":false,"format":"[%s][%s] %s","realtime_write":false}}}`,
	}
	for name, content := range requiredConfig {
		if err := os.WriteFile(filepath.Join(rootConfigPath, name), []byte(content), 0o600); err != nil {
			t.Fatalf("写入必需配置 %s 失败: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(applicationConfigPath, "app.json"), []byte(`{"app_debug":true,"app_name":"nested"}`), 0o600); err != nil {
		t.Fatalf("写入应用配置失败: %v", err)
	}

	app := NewConsoleAppUninitialized(basePath)
	if err := app.Initialize(); err != nil {
		t.Fatalf("初始化单应用失败: %v", err)
	}
	if app.config.GetBool("app.app_debug") {
		t.Fatal("app/config 不应覆盖根 config 的 app_debug")
	}
	if got := app.config.GetString("app.app_name"); got != "root" {
		t.Fatalf("单应用应保留根 config 值: got %q, want root", got)
	}
}
