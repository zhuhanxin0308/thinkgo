package framework

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewAppUninitializedDefersRuntimeAssembly 验证未初始化构造入口不会提前装配运行时资源。
func TestNewAppUninitializedDefersRuntimeAssembly(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	if app == nil {
		t.Fatal("未初始化构造入口不应返回 nil")
	}
	t.Cleanup(func() { _ = app.Close() })

	if app.Initialized() {
		t.Fatal("未初始化构造入口不应标记为已初始化")
	}
	if app.State() != ApplicationStateConstructed {
		t.Fatalf("未初始化构造入口状态错误: %v", app.State())
	}
	if app.cache != nil || app.session != nil || app.dbManager != nil {
		t.Fatalf("未初始化构造入口不应装配运行时资源: cache=%v session=%v db=%v", app.cache, app.session, app.dbManager)
	}
}

// TestNewAppBindsFoundationServices 验证构造阶段基础服务具有统一容器身份。
func TestNewAppBindsFoundationServices(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })

	expected := map[string]interface{}{
		"app":        app,
		"container":  app.container,
		"config":     app.config,
		"env":        app.env,
		"event":      app.event,
		"route":      app.route,
		"middleware": app.middleware,
		"metrics":    app.metrics,
		"health":     app.health,
		"debug":      app.debug,
	}
	for name, expectedInstance := range expected {
		instance, err := app.Make(name)
		if err != nil {
			t.Fatalf("解析基础服务 %q 失败: %v", name, err)
		}
		if instance != expectedInstance {
			t.Fatalf("基础服务 %q 与 App 字段不一致: expected=%p actual=%p", name, expectedInstance, instance)
		}
	}
}

// TestNewAppUninitializedDoesNotCreateLogDriver 验证未初始化构造入口不会提前创建文件日志资源。
func TestNewAppUninitializedDoesNotCreateLogDriver(t *testing.T) {
	basePath := t.TempDir()
	app := NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })

	app.log.Info("构造阶段日志占位测试")
	if err := app.log.Flush(context.Background()); err != nil {
		t.Fatalf("刷新构造阶段日志失败: %v", err)
	}
	logPath := filepath.Join(basePath, "runtime", "log")
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("未初始化应用不应提前创建日志目录，实际错误为 %v", err)
	}
}

// TestBuildAppRejectsInitializationFailure 验证显式构建失败时不会返回半初始化应用对象。
func TestBuildAppRejectsInitializationFailure(t *testing.T) {
	basePath := t.TempDir()
	configPath := filepath.Join(basePath, "config")
	if err := os.MkdirAll(configPath, 0o700); err != nil {
		t.Fatalf("创建测试配置目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configPath, "cache.json"), []byte(`{"default":"missing","stores":{}}`), 0o600); err != nil {
		t.Fatalf("写入非法缓存配置失败: %v", err)
	}

	app, err := BuildApp(basePath)
	if app != nil {
		t.Fatal("显式构建失败时不应返回半初始化应用对象")
	}
	if err == nil || !strings.Contains(err.Error(), "缓存配置") {
		t.Fatalf("显式构建应返回缓存配置错误，实际为 %v", err)
	}
}

// TestBuildAppReturnsInitializedApplication 验证显式构建成功后返回完整初始化应用。
func TestBuildAppReturnsInitializedApplication(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)

	app, err := BuildApp(basePath)
	if err != nil {
		t.Fatalf("显式构建应用失败: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	if !app.Initialized() {
		t.Fatal("显式构建成功后应用应标记为已初始化")
	}
	if app.State() != ApplicationStateInitialized {
		t.Fatalf("显式构建成功后应用状态错误: %v", app.State())
	}
}
