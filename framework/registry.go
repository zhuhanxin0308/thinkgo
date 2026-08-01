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
	// ErrApplicationRegistrationClosed 表示应用运行时装配已完成，不再接受不会自动生效的组件注册。
	ErrApplicationRegistrationClosed = errors.New("应用组件注册阶段已结束")
)

type applicationRegistry struct {
	lock         sync.RWMutex
	controllers  map[string]reflect.Type
	routeLoaders []RouteLoader
	middlewares  []middleware.Handler
}

type applicationRegistrySnapshot struct {
	controllers  map[string]reflect.Type
	routeLoaders []RouteLoader
	middlewares  []middleware.Handler
}

func (registry *applicationRegistry) registerController(name string, controller interface{}) error {
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

	registry.lock.Lock()
	defer registry.lock.Unlock()
	if registry.controllers == nil {
		registry.controllers = make(map[string]reflect.Type)
	}
	if _, exists := registry.controllers[name]; exists {
		return fmt.Errorf("%w: 控制器 %q", ErrDuplicateRegistration, name)
	}
	registry.controllers[name] = controllerType
	return nil
}

func (registry *applicationRegistry) registerRouteLoader(loader RouteLoader) error {
	if loader == nil {
		return fmt.Errorf("%w: 路由加载器不能为空", ErrInvalidRegistration)
	}
	registry.lock.Lock()
	registry.routeLoaders = append(registry.routeLoaders, loader)
	registry.lock.Unlock()
	return nil
}

func (registry *applicationRegistry) registerGlobalMiddleware(handler middleware.Handler) error {
	if handler == nil {
		return fmt.Errorf("%w: 全局中间件不能为空", ErrInvalidRegistration)
	}
	registry.lock.Lock()
	registry.middlewares = append(registry.middlewares, handler)
	registry.lock.Unlock()
	return nil
}

func (registry *applicationRegistry) snapshotControllers() map[string]reflect.Type {
	registry.lock.RLock()
	defer registry.lock.RUnlock()
	snapshot := make(map[string]reflect.Type, len(registry.controllers))
	for name, controllerType := range registry.controllers {
		snapshot[name] = controllerType
	}
	return snapshot
}

func (registry *applicationRegistry) snapshotRouteLoaders() []RouteLoader {
	registry.lock.RLock()
	defer registry.lock.RUnlock()
	snapshot := make([]RouteLoader, len(registry.routeLoaders))
	copy(snapshot, registry.routeLoaders)
	return snapshot
}

func (registry *applicationRegistry) snapshotMiddlewares() []middleware.Handler {
	registry.lock.RLock()
	defer registry.lock.RUnlock()
	return append([]middleware.Handler(nil), registry.middlewares...)
}

// snapshot 一次性复制应用注册表，保证多应用构造看到同一注册时刻的完整集合。
func (registry *applicationRegistry) snapshot() applicationRegistrySnapshot {
	if registry == nil {
		return applicationRegistrySnapshot{}
	}
	registry.lock.RLock()
	defer registry.lock.RUnlock()
	controllers := make(map[string]reflect.Type, len(registry.controllers))
	for name, controllerType := range registry.controllers {
		controllers[name] = controllerType
	}
	return applicationRegistrySnapshot{
		controllers:  controllers,
		routeLoaders: append([]RouteLoader(nil), registry.routeLoaders...),
		middlewares:  append([]middleware.Handler(nil), registry.middlewares...),
	}
}

func (registry *applicationRegistry) cloneFrom(source *applicationRegistry) {
	if source == nil || registry == source {
		return
	}
	snapshot := source.snapshot()

	registry.lock.Lock()
	registry.controllers = snapshot.controllers
	registry.routeLoaders = snapshot.routeLoaders
	registry.middlewares = snapshot.middlewares
	registry.lock.Unlock()
}

// RouteLoader 定义可返回初始化错误的应用路由加载器。
type RouteLoader func(app *App) error

var globalApplicationRegistry = applicationRegistry{
	controllers: make(map[string]reflect.Type),
}

// RegisterController 注册控制器类型；同名注册不会覆盖已有绑定。
func RegisterController(name string, controller interface{}) error {
	return globalApplicationRegistry.registerController(name, controller)
}

// MustRegisterController 在包初始化阶段注册控制器，程序员配置错误会立即 panic。
func MustRegisterController(name string, controller interface{}) {
	if err := RegisterController(name, controller); err != nil {
		panic(err)
	}
}

// RegisterRouteLoader 注册非空路由加载器。
func RegisterRouteLoader(loader RouteLoader) error {
	return globalApplicationRegistry.registerRouteLoader(loader)
}

// MustRegisterRouteLoader 在包初始化阶段注册路由加载器。
func MustRegisterRouteLoader(loader RouteLoader) {
	if err := RegisterRouteLoader(loader); err != nil {
		panic(err)
	}
}

// RegisterGlobalMiddleware 注册非空全局中间件。
func RegisterGlobalMiddleware(handler middleware.Handler) error {
	return globalApplicationRegistry.registerGlobalMiddleware(handler)
}

// MustRegisterGlobalMiddleware 在包初始化阶段注册全局中间件。
func MustRegisterGlobalMiddleware(handler middleware.Handler) {
	if err := RegisterGlobalMiddleware(handler); err != nil {
		panic(err)
	}
}

func snapshotControllerRegistry() map[string]reflect.Type {
	return globalApplicationRegistry.snapshotControllers()
}

func snapshotRouteRegistry() []RouteLoader {
	return globalApplicationRegistry.snapshotRouteLoaders()
}

func snapshotMiddlewareRegistry() []middleware.Handler {
	return globalApplicationRegistry.snapshotMiddlewares()
}

func unregisterController(name string) {
	globalApplicationRegistry.lock.Lock()
	delete(globalApplicationRegistry.controllers, name)
	globalApplicationRegistry.lock.Unlock()
}

// RegisterController 在当前应用实例中注册控制器类型。
// 多应用运行时只消费此实例注册表，不读取其他 App 的控制器绑定。
func (app *App) RegisterController(name string, controller interface{}) error {
	if app == nil {
		return ErrNilApplication
	}
	app.registrationMu.RLock()
	defer app.registrationMu.RUnlock()
	if err := app.ensureRegistrationOpen(); err != nil {
		return err
	}
	return app.registry.registerController(name, controller)
}

// RegisterRouteLoader 在当前应用实例中注册路由加载器。
func (app *App) RegisterRouteLoader(loader RouteLoader) error {
	if app == nil {
		return ErrNilApplication
	}
	app.registrationMu.RLock()
	defer app.registrationMu.RUnlock()
	if err := app.ensureRegistrationOpen(); err != nil {
		return err
	}
	return app.registry.registerRouteLoader(loader)
}

// RegisterGlobalMiddleware 在当前应用实例中注册全局中间件。
func (app *App) RegisterGlobalMiddleware(handler middleware.Handler) error {
	if app == nil {
		return ErrNilApplication
	}
	app.registrationMu.RLock()
	defer app.registrationMu.RUnlock()
	if err := app.ensureRegistrationOpen(); err != nil {
		return err
	}
	return app.registry.registerGlobalMiddleware(handler)
}

// ensureRegistrationOpen 在修改注册表前检查应用生命周期，避免注册结果无法进入运行时装配。
func (app *App) ensureRegistrationOpen() error {
	if app == nil {
		return ErrNilApplication
	}
	if app.registrationClosed {
		return ErrApplicationRegistrationClosed
	}
	app.lifecycle.lock.Lock()
	defer app.lifecycle.lock.Unlock()
	if app.lifecycle.closed || app.lifecycle.state >= ApplicationStateClosing {
		return ErrApplicationClosed
	}
	if app.lifecycle.initialized || app.lifecycle.state >= ApplicationStateInitialized {
		return ErrApplicationRegistrationClosed
	}
	return nil
}

// closeApplicationRegistration 在最终装配开始前关闭组件注册窗口，确保快照与写入不会交错。
func (app *App) closeApplicationRegistration() {
	if app == nil {
		return
	}
	app.registrationMu.Lock()
	app.registrationClosed = true
	app.registrationMu.Unlock()
}

func (app *App) snapshotControllerRegistry() map[string]reflect.Type {
	if app == nil {
		return map[string]reflect.Type{}
	}
	return app.registry.snapshotControllers()
}

func (app *App) snapshotRouteRegistry() []RouteLoader {
	if app == nil {
		return nil
	}
	return app.registry.snapshotRouteLoaders()
}

func (app *App) snapshotMiddlewareRegistry() []middleware.Handler {
	if app == nil {
		return nil
	}
	return app.registry.snapshotMiddlewares()
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
