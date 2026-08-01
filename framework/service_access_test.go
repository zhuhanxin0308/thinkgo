package framework

import (
	"errors"
	"testing"

	"thinkgo/framework/cache"
)

// TestResolveServiceUsesStableServiceNames 验证应用通过公开服务名称解析已装配服务。
func TestResolveServiceUsesStableServiceNames(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })

	tests := []struct {
		name     ServiceName
		expected interface{}
	}{
		{name: ServiceApp, expected: app},
		{name: ServiceContainer, expected: app.container},
		{name: ServiceConfig, expected: app.config},
		{name: ServiceEnv, expected: app.env},
		{name: ServiceRoute, expected: app.route},
		{name: ServiceMiddleware, expected: app.middleware},
	}
	for _, test := range tests {
		actual, err := app.ResolveService(test.name)
		if err != nil {
			t.Fatalf("解析基础服务 %q 失败: %v", test.name, err)
		}
		if actual != test.expected {
			t.Fatalf("服务 %q 未返回容器中的同一实例: expected=%p actual=%p", test.name, test.expected, actual)
		}
	}

	if _, err := app.ResolveService(ServiceCache); !errors.Is(err, ErrServiceNotReady) {
		t.Fatalf("未初始化应用解析运行时服务应返回就绪状态错误，实际为 %v", err)
	}
}

// TestResolveServiceAsChecksServiceType 验证类型安全解析不会静默接受错误的服务类型。
func TestResolveServiceAsChecksServiceType(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app, err := BuildApp(basePath)
	if err != nil {
		t.Fatalf("构建测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	cacheService, err := ResolveServiceAs[*cache.Cache](app, ServiceCache)
	if err != nil {
		t.Fatalf("类型安全解析缓存服务失败: %v", err)
	}
	if cacheService != app.cache {
		t.Fatalf("类型安全解析结果与应用字段不一致: expected=%p actual=%p", app.cache, cacheService)
	}

	if _, err := ResolveServiceAs[*cache.Cache](app, ServiceConfig); !errors.Is(err, ErrServiceTypeMismatch) {
		t.Fatalf("错误服务类型应返回类型不匹配错误，实际为 %v", err)
	}
}

// TestResolveServiceRejectsInvalidState 验证 nil 应用、非法服务名和关闭状态不会继续访问容器。
func TestResolveServiceRejectsInvalidState(t *testing.T) {
	var nilApp *App
	if _, err := nilApp.ResolveService(ServiceConfig); !errors.Is(err, ErrNilApplication) {
		t.Fatalf("nil 应用应返回空应用错误，实际为 %v", err)
	}

	app := NewAppUninitialized(t.TempDir())
	if _, err := app.ResolveService(ServiceName(" ")); !errors.Is(err, ErrInvalidServiceName) {
		t.Fatalf("非法服务名应返回服务名错误，实际为 %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("关闭测试应用失败: %v", err)
	}
	if _, err := app.ResolveService(ServiceConfig); !errors.Is(err, ErrApplicationClosed) {
		t.Fatalf("关闭应用解析服务应返回应用已关闭错误，实际为 %v", err)
	}
}
