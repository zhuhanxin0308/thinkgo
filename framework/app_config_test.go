package framework

import (
	"os"
	"path/filepath"
	"testing"

	"thinkgo/framework/config"
)

func TestApplicationConfigOverlaysProjectConfig(t *testing.T) {
	basePath := t.TempDir()
	applicationPath := filepath.Join(basePath, "app", "admin")
	rootConfigPath := filepath.Join(basePath, "config")
	applicationConfigPath := filepath.Join(applicationPath, "config")
	for _, directory := range []string{rootConfigPath, applicationConfigPath} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("创建配置目录失败: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(rootConfigPath, "app.json"), []byte(`{"app_debug":false,"default_app":"index"}`), 0o600); err != nil {
		t.Fatalf("写入项目配置失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(applicationConfigPath, "app.json"), []byte(`{"app_debug":true}`), 0o600); err != nil {
		t.Fatalf("写入应用配置失败: %v", err)
	}

	app := newIsolatedPathTestApp(basePath, "admin")
	app.config = config.NewConfig()
	if err := app.config.LoadAll(rootConfigPath); err != nil {
		t.Fatalf("加载项目配置失败: %v", err)
	}
	if err := app.loadApplicationConfig(); err != nil {
		t.Fatalf("加载应用配置失败: %v", err)
	}

	if !app.config.GetBool("app.app_debug") {
		t.Fatal("应用配置应覆盖项目级 app_debug")
	}
	if got := app.config.GetString("app.default_app"); got != "index" {
		t.Fatalf("应用配置覆盖不应删除项目级字段: got %q", got)
	}
}

func TestMissingApplicationConfigDoesNotBlockInitialization(t *testing.T) {
	app := newIsolatedPathTestApp(t.TempDir(), "admin")
	app.config = config.NewConfig()
	if err := app.loadApplicationConfig(); err != nil {
		t.Fatalf("缺少可选应用配置目录不应失败: %v", err)
	}
}
