package framework

import "testing"

// mustBuildTestApp 构建并返回已完成初始化的测试应用。
func mustBuildTestApp(t *testing.T, basePath string) *App {
	t.Helper()
	app, err := BuildApp(basePath)
	if err != nil {
		t.Fatalf("构建测试应用失败: %v", err)
	}
	return app
}

// mustBuildTestConsoleApp 构建并返回跳过数据库连接的测试控制台应用。
func mustBuildTestConsoleApp(t *testing.T, basePath string) *App {
	t.Helper()
	app, err := BuildConsoleApp(basePath)
	if err != nil {
		t.Fatalf("构建测试控制台应用失败: %v", err)
	}
	return app
}

// initializeTestApp 显式构造并初始化测试应用，供需要检查初始化错误的场景使用。
func initializeTestApp(t *testing.T, basePath string) (*App, error) {
	t.Helper()
	app := NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	return app, app.Initialize()
}

// initializeTestConsoleApp 显式构造并初始化测试控制台应用，供需要检查初始化错误的场景使用。
func initializeTestConsoleApp(t *testing.T, basePath string) (*App, error) {
	t.Helper()
	app := NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	return app, app.Initialize()
}
