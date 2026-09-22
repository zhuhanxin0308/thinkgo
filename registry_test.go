package framework

import (
	"errors"
	"testing"
)

// TestRouteLoaderPanicBecomesStartupError 验证路由注册回调 panic 不会直接击穿应用构造过程。
func TestRouteLoaderPanicBecomesStartupError(t *testing.T) {
	err := safeLoadRoutes(func(app *App) error { panic("route panic") }, &App{})
	if !errors.Is(err, ErrRegistrationCallbackPanic) {
		t.Fatalf("路由加载 panic 应返回 ErrRegistrationCallbackPanic，实际为 %v", err)
	}
	loaderErr := errors.New("route load failed")
	if err := safeLoadRoutes(func(app *App) error { return loaderErr }, &App{}); !errors.Is(err, loaderErr) {
		t.Fatalf("路由加载错误应原样返回，实际为 %v", err)
	}
}
