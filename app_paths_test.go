package framework

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSingleApplicationPathsMatchThinkPHPDirectories(t *testing.T) {
	basePath := t.TempDir()
	app := NewAppUninitialized(basePath)
	want := map[string]string{
		"root":    basePath,
		"base":    filepath.Join(basePath, "app"),
		"app":     filepath.Join(basePath, "app"),
		"config":  filepath.Join(basePath, "config"),
		"route":   filepath.Join(basePath, "route"),
		"lang":    filepath.Join(basePath, "app", "lang"),
		"view":    filepath.Join(basePath, "view"),
		"runtime": filepath.Join(basePath, "runtime"),
	}
	got := map[string]string{
		"root":    app.GetRootPath(),
		"base":    app.GetBasePath(),
		"app":     app.GetAppPath(),
		"config":  app.GetConfigPath(),
		"route":   app.GetRoutePath(),
		"lang":    app.ApplicationLangPath(),
		"view":    app.ApplicationViewPath(),
		"runtime": app.ApplicationRuntimePath(),
	}
	for name, expected := range want {
		if actual := got[name]; actual != expected {
			t.Fatalf("%s 路径错误: got %q, want %q", name, actual, expected)
		}
	}
}

func TestApplicationStoragePathRejectsEscapeAndScopesRelativePaths(t *testing.T) {
	basePath := t.TempDir()
	app := NewAppUninitialized(basePath)

	resolved, err := app.resolveStoragePath(filepath.ToSlash(filepath.Join(".", "runtime", "cache")))
	if err != nil {
		t.Fatalf("默认运行时缓存路径解析失败: %v", err)
	}
	if expected := filepath.Join(app.ApplicationRuntimePath(), "cache"); resolved != expected {
		t.Fatalf("默认运行时路径未归属当前应用: got %q, want %q", resolved, expected)
	}

	outsideCases := []string{"../outside", "runtime/../outside", "runtime\tcache"}
	for _, configuredPath := range outsideCases {
		if _, err := app.resolveStoragePath(configuredPath); err == nil {
			t.Fatalf("非法运行时路径应返回错误: %q", configuredPath)
		}
	}

	externalPath := filepath.Join(basePath, "external-cache")
	resolved, err = app.resolveStoragePath(externalPath)
	if err != nil {
		t.Fatalf("显式绝对路径解析失败: %v", err)
	}
	if resolved != externalPath {
		t.Fatalf("显式绝对路径不应被改写: got %q, want %q", resolved, externalPath)
	}
	if _, err := os.Stat(resolved); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("路径解析不应提前创建目录: %v", err)
	}
}

func TestSetAppPathChangesOnlyExplicitApplicationDirectory(t *testing.T) {
	basePath := t.TempDir()
	app := NewAppUninitialized(basePath)
	customPath := filepath.Join(basePath, "custom-app")
	if err := app.SetAppPath(customPath); err != nil {
		t.Fatalf("设置应用目录失败: %v", err)
	}
	if app.GetAppPath() != customPath {
		t.Fatalf("应用目录未更新: got %q, want %q", app.GetAppPath(), customPath)
	}
	if app.GetConfigPath() != filepath.Join(basePath, "config") {
		t.Fatal("显式应用目录不能改变根配置目录")
	}
	if app.ApplicationViewPath() != filepath.Join(basePath, "view") {
		t.Fatal("单应用视图目录必须保持为根 view")
	}
}

// TestNativeApplicationPathsMatchThinkPHPMultiApp 验证应用选择完成前后，
// route、view 和 runtime 都稳定指向 app/<name> 与 runtime/<name>。
func TestNativeApplicationPathsMatchThinkPHPMultiApp(t *testing.T) {
	basePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(basePath, "app", "index"), 0o755); err != nil {
		t.Fatalf("创建原生应用目录失败: %v", err)
	}
	app := NewAppUninitialized(basePath)
	if err := app.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
	); err != nil {
		t.Fatalf("注册原生应用失败: %v", err)
	}
	if got := app.GetRoutePath(); got != filepath.Join(basePath, "app", "index", "route") {
		t.Fatalf("原生应用路由目录错误: %q", got)
	}
	if got := app.ApplicationViewPath(); got != filepath.Join(basePath, "app", "index", "view") {
		t.Fatalf("原生应用视图目录错误: %q", got)
	}
	if err := app.SetRuntimePath(filepath.Join(basePath, "runtime", "index")); err != nil {
		t.Fatalf("设置原生应用运行目录失败: %v", err)
	}
	if got := app.GetRuntimePath(); got != filepath.Join(basePath, "runtime", "index") {
		t.Fatalf("原生应用运行目录错误: %q", got)
	}
}
