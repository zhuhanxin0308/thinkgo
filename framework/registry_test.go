package framework

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	frameworkcontext "thinkgo/framework/context"
)

type registryTestController struct{}

// TestApplicationRegistryRejectsInvalidAndDuplicateControllers 验证全局注册表不会因非法原型或覆盖注册而损坏。
func TestApplicationRegistryRejectsInvalidAndDuplicateControllers(t *testing.T) {
	if err := RegisterController("", &registryTestController{}); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("空控制器名应返回 ErrInvalidRegistration，实际为 %v", err)
	}
	if err := RegisterController("invalid", nil); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("nil 控制器应返回 ErrInvalidRegistration，实际为 %v", err)
	}
	if err := RegisterController("invalid", 42); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("非结构体控制器应返回 ErrInvalidRegistration，实际为 %v", err)
	}

	name := "__registry_duplicate_controller__"
	defer unregisterController(name)
	if err := RegisterController(name, &registryTestController{}); err != nil {
		t.Fatalf("注册控制器失败: %v", err)
	}
	if err := RegisterController(name, &registryTestController{}); !errors.Is(err, ErrDuplicateRegistration) {
		t.Fatalf("重复控制器应返回 ErrDuplicateRegistration，实际为 %v", err)
	}
}

// TestApplicationRegistrySupportsConcurrentControllerRegistration 验证注册与快照并发时不会读写同一 map。
func TestApplicationRegistrySupportsConcurrentControllerRegistration(t *testing.T) {
	const registrations = 64
	names := make([]string, registrations)
	for index := range names {
		names[index] = fmt.Sprintf("__registry_concurrent_%d__", index)
		defer unregisterController(names[index])
	}

	errCh := make(chan error, registrations)
	var waitGroup sync.WaitGroup
	for _, name := range names {
		name := name
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if err := RegisterController(name, &registryTestController{}); err != nil {
				errCh <- err
			}
			_ = snapshotControllerRegistry()
		}()
	}
	waitGroup.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	snapshot := snapshotControllerRegistry()
	for _, name := range names {
		if _, exists := snapshot[name]; !exists {
			t.Fatalf("并发注册后缺少控制器 %q", name)
		}
	}
}

// TestApplicationRegistryRejectsNilCallbacks 验证路由加载器和全局中间件不会接受 nil。
func TestApplicationRegistryRejectsNilCallbacks(t *testing.T) {
	if err := RegisterRouteLoader(nil); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("nil 路由加载器应返回 ErrInvalidRegistration，实际为 %v", err)
	}
	if err := RegisterGlobalMiddleware(nil); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("nil 全局中间件应返回 ErrInvalidRegistration，实际为 %v", err)
	}
}

// TestApplicationRegistrySnapshotsValidCallbacks 验证有效路由和中间件通过 Must API 注册并以副本读取。
func TestApplicationRegistrySnapshotsValidCallbacks(t *testing.T) {
	originalRoutes := snapshotRouteRegistry()
	originalMiddlewares := snapshotMiddlewareRegistry()
	defer func() {
		globalApplicationRegistry.lock.Lock()
		globalApplicationRegistry.routeLoaders = originalRoutes
		globalApplicationRegistry.middlewares = originalMiddlewares
		globalApplicationRegistry.lock.Unlock()
	}()

	loader := RouteLoader(func(app *App) error { return nil })
	handler := func(request *frameworkcontext.Request, next func(*frameworkcontext.Request) *frameworkcontext.Response) *frameworkcontext.Response {
		return next(request)
	}
	MustRegisterRouteLoader(loader)
	MustRegisterGlobalMiddleware(handler)
	if len(snapshotRouteRegistry()) != len(originalRoutes)+1 {
		t.Fatal("路由加载器快照应包含新注册项")
	}
	if len(snapshotMiddlewareRegistry()) != len(originalMiddlewares)+1 {
		t.Fatal("全局中间件快照应包含新注册项")
	}
}

// TestMustRegisterControllerPanicsOnProgrammerError 验证 Must API 在 init 阶段立即暴露错误配置。
func TestMustRegisterControllerPanicsOnProgrammerError(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustRegisterController 遇到非法配置时必须 panic")
		}
	}()
	MustRegisterController("", &registryTestController{})
}

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
