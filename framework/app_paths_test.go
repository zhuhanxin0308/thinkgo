package framework

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newIsolatedPathTestApp(basePath, name string) *App {
	return &App{
		BasePath:        basePath,
		ApplicationName: name,
		ApplicationPath: filepath.Join(basePath, "app", name),
		RuntimePath:     filepath.Join(basePath, "runtime", name),
	}
}

func TestApplicationPathsKeepApplicationResourcesIsolated(t *testing.T) {
	basePath := t.TempDir()
	indexApp := newIsolatedPathTestApp(basePath, "index")
	adminApp := newIsolatedPathTestApp(basePath, "admin")

	if indexApp.ApplicationConfigPath() == adminApp.ApplicationConfigPath() {
		t.Fatal("不同应用的配置目录不能相同")
	}
	if indexApp.ApplicationLangPath() == adminApp.ApplicationLangPath() {
		t.Fatal("不同应用的语言目录不能相同")
	}
	if indexApp.ApplicationViewPath() == adminApp.ApplicationViewPath() {
		t.Fatal("不同应用的视图目录不能相同")
	}
	if indexApp.ApplicationRuntimePath() == adminApp.ApplicationRuntimePath() {
		t.Fatal("不同应用的运行时目录不能相同")
	}
	if indexApp.RuntimeLogPath() == adminApp.RuntimeLogPath() || indexApp.RuntimeCachePath() == adminApp.RuntimeCachePath() || indexApp.RuntimeSessionPath() == adminApp.RuntimeSessionPath() {
		t.Fatal("不同应用的日志、缓存和会话目录不能相同")
	}
	if indexApp.ProjectPublicPath() != adminApp.ProjectPublicPath() {
		t.Fatal("公共静态目录必须属于宿主项目根目录")
	}
	if indexApp.ExceptionTemplatePath() != adminApp.ExceptionTemplatePath() {
		t.Fatal("异常模板目录必须属于宿主项目根目录")
	}

	for _, path := range []string{
		indexApp.ApplicationConfigPath(),
		indexApp.ApplicationLangPath(),
		indexApp.ApplicationViewPath(),
		indexApp.ApplicationRuntimePath(),
	} {
		if filepath.IsAbs(path) == false {
			t.Fatalf("应用资源路径必须是绝对路径: %q", path)
		}
	}
}

func TestApplicationStoragePathRejectsEscapeAndScopesRelativePaths(t *testing.T) {
	basePath := t.TempDir()
	app := newIsolatedPathTestApp(basePath, "admin")

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

func TestSingleAppPathsUseIndexApplicationDirectory(t *testing.T) {
	basePath := t.TempDir()
	app := &App{
		BasePath:        basePath,
		ApplicationName: "index",
		ApplicationPath: filepath.Join(basePath, "app", "index"),
		RuntimePath:     filepath.Join(basePath, "runtime"),
	}
	if expected := filepath.Join(basePath, "app", "index", "lang"); app.ApplicationLangPath() != expected {
		t.Fatalf("单应用语言资源路径错误: got %q, want %q", app.ApplicationLangPath(), expected)
	}
	if expected := filepath.Join(basePath, "app", "index", "view"); app.ApplicationViewPath() != expected {
		t.Fatalf("单应用视图资源路径错误: got %q, want %q", app.ApplicationViewPath(), expected)
	}
}
