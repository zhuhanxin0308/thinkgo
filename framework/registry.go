package framework

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unicode"

	"thinkgo/framework/middleware"
)

const maxRegistrationNameBytes = 256

var (
	// ErrInvalidRegistration 表示应用组件名称、原型或回调无效。
	ErrInvalidRegistration = errors.New("无效应用组件注册")
	// ErrDuplicateRegistration 表示同名组件已注册，禁止静默覆盖。
	ErrDuplicateRegistration = errors.New("应用组件重复注册")
	// ErrRegistrationCallbackPanic 表示注册回调 panic 已转换为启动错误。
	ErrRegistrationCallbackPanic = errors.New("应用组件注册回调发生 panic")
)

type applicationRegistry struct {
	lock         sync.RWMutex
	controllers  map[string]reflect.Type
	routeLoaders []RouteLoader
	middlewares  []middleware.Handler
}

// RouteLoader 定义可返回初始化错误的应用路由加载器。
type RouteLoader func(app *App) error

var globalApplicationRegistry = applicationRegistry{
	controllers: make(map[string]reflect.Type),
}

// RegisterController 注册控制器类型；同名注册不会覆盖已有绑定。
func RegisterController(name string, controller interface{}) error {
	if err := validateRegistrationName(name); err != nil {
		return err
	}
	controllerType := reflect.TypeOf(controller)
	if controllerType == nil {
		return fmt.Errorf("%w: 控制器原型不能为空", ErrInvalidRegistration)
	}
	if controllerType.Kind() == reflect.Pointer {
		controllerType = controllerType.Elem()
	}
	if controllerType.Kind() != reflect.Struct {
		return fmt.Errorf("%w: 控制器 %q 必须是结构体或结构体指针", ErrInvalidRegistration, name)
	}

	globalApplicationRegistry.lock.Lock()
	defer globalApplicationRegistry.lock.Unlock()
	if globalApplicationRegistry.controllers == nil {
		globalApplicationRegistry.controllers = make(map[string]reflect.Type)
	}
	if _, exists := globalApplicationRegistry.controllers[name]; exists {
		return fmt.Errorf("%w: 控制器 %q", ErrDuplicateRegistration, name)
	}
	globalApplicationRegistry.controllers[name] = controllerType
	return nil
}

// MustRegisterController 在包初始化阶段注册控制器，程序员配置错误会立即 panic。
func MustRegisterController(name string, controller interface{}) {
	if err := RegisterController(name, controller); err != nil {
		panic(err)
	}
}

// RegisterRouteLoader 注册非空路由加载器。
func RegisterRouteLoader(loader RouteLoader) error {
	if loader == nil {
		return fmt.Errorf("%w: 路由加载器不能为空", ErrInvalidRegistration)
	}
	globalApplicationRegistry.lock.Lock()
	globalApplicationRegistry.routeLoaders = append(globalApplicationRegistry.routeLoaders, loader)
	globalApplicationRegistry.lock.Unlock()
	return nil
}

// MustRegisterRouteLoader 在包初始化阶段注册路由加载器。
func MustRegisterRouteLoader(loader RouteLoader) {
	if err := RegisterRouteLoader(loader); err != nil {
		panic(err)
	}
}

// RegisterGlobalMiddleware 注册非空全局中间件。
func RegisterGlobalMiddleware(handler middleware.Handler) error {
	if handler == nil {
		return fmt.Errorf("%w: 全局中间件不能为空", ErrInvalidRegistration)
	}
	globalApplicationRegistry.lock.Lock()
	globalApplicationRegistry.middlewares = append(globalApplicationRegistry.middlewares, handler)
	globalApplicationRegistry.lock.Unlock()
	return nil
}

// MustRegisterGlobalMiddleware 在包初始化阶段注册全局中间件。
func MustRegisterGlobalMiddleware(handler middleware.Handler) {
	if err := RegisterGlobalMiddleware(handler); err != nil {
		panic(err)
	}
}

func snapshotControllerRegistry() map[string]reflect.Type {
	globalApplicationRegistry.lock.RLock()
	defer globalApplicationRegistry.lock.RUnlock()
	snapshot := make(map[string]reflect.Type, len(globalApplicationRegistry.controllers))
	for name, controllerType := range globalApplicationRegistry.controllers {
		snapshot[name] = controllerType
	}
	return snapshot
}

func snapshotRouteRegistry() []RouteLoader {
	globalApplicationRegistry.lock.RLock()
	defer globalApplicationRegistry.lock.RUnlock()
	snapshot := make([]RouteLoader, len(globalApplicationRegistry.routeLoaders))
	copy(snapshot, globalApplicationRegistry.routeLoaders)
	return snapshot
}

func snapshotMiddlewareRegistry() []middleware.Handler {
	globalApplicationRegistry.lock.RLock()
	defer globalApplicationRegistry.lock.RUnlock()
	return append([]middleware.Handler(nil), globalApplicationRegistry.middlewares...)
}

func unregisterController(name string) {
	globalApplicationRegistry.lock.Lock()
	delete(globalApplicationRegistry.controllers, name)
	globalApplicationRegistry.lock.Unlock()
}

func validateRegistrationName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || len(name) > maxRegistrationNameBytes {
		return fmt.Errorf("%w: 组件名称 %q 无效", ErrInvalidRegistration, name)
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: 组件名称包含控制字符", ErrInvalidRegistration)
		}
	}
	return nil
}

func safeLoadRoutes(loader RouteLoader, app *App) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", ErrRegistrationCallbackPanic, recovered)
		}
	}()
	return loader(app)
}
