package framework

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode"

	requestbinding "github.com/zhuhanxin0308/thinkgo/framework/binding"
	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
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
	lock              sync.RWMutex
	applicationLoader ApplicationLoader
	controllers       map[string]reflect.Type
	models            map[string]reflect.Type
	modelBindings     map[reflect.Type]string
	routeLoaders      []RouteLoader
	middlewares       []middleware.Handler
	appMiddlewares    []middleware.Handler
}

func (registry *applicationRegistry) registerApplicationLoader(loader ApplicationLoader) error {
	if loader == nil {
		return fmt.Errorf("%w: 应用定义加载器不能为空", ErrInvalidRegistration)
	}
	registry.lock.Lock()
	defer registry.lock.Unlock()
	if registry.applicationLoader != nil {
		return fmt.Errorf("%w: 应用定义加载器", ErrDuplicateRegistration)
	}
	registry.applicationLoader = loader
	return nil
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
	methods := reflect.PointerTo(controllerType)
	for index := 0; index < methods.NumMethod(); index++ {
		method := methods.Method(index)
		if err := requestbinding.ValidateSignature(method.Type); err != nil {
			return fmt.Errorf("%w: 控制器 %s.%s: %w", ErrInvalidRegistration, name, method.Name, err)
		}
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

func (registry *applicationRegistry) registerModel(name string, model interface{}) error {
	if err := validateRegistrationName(name); err != nil {
		return err
	}
	modelType := reflect.TypeOf(model)
	if modelType == nil {
		return fmt.Errorf("%w: 模型原型不能为空", ErrInvalidRegistration)
	}
	for modelType.Kind() == reflect.Pointer {
		modelType = modelType.Elem()
	}
	if modelType.Kind() != reflect.Struct {
		return fmt.Errorf("%w: 模型 %q 必须是结构体或结构体指针", ErrInvalidRegistration, name)
	}

	registry.lock.Lock()
	defer registry.lock.Unlock()
	if registry.models == nil {
		registry.models = make(map[string]reflect.Type)
	}
	if _, exists := registry.models[name]; exists {
		return fmt.Errorf("%w: 模型 %q", ErrDuplicateRegistration, name)
	}
	registry.models[name] = modelType
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

func (registry *applicationRegistry) registerApplicationMiddleware(handler middleware.Handler) error {
	if handler == nil {
		return fmt.Errorf("%w: 应用中间件不能为空", ErrInvalidRegistration)
	}
	registry.lock.Lock()
	registry.appMiddlewares = append(registry.appMiddlewares, handler)
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

func (registry *applicationRegistry) snapshotModels() map[string]reflect.Type {
	registry.lock.RLock()
	defer registry.lock.RUnlock()
	snapshot := make(map[string]reflect.Type, len(registry.models))
	for name, modelType := range registry.models {
		snapshot[name] = modelType
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

func (registry *applicationRegistry) snapshotApplicationMiddlewares() []middleware.Handler {
	registry.lock.RLock()
	defer registry.lock.RUnlock()
	return append([]middleware.Handler(nil), registry.appMiddlewares...)
}

func (registry *applicationRegistry) snapshotApplicationLoader() ApplicationLoader {
	registry.lock.RLock()
	defer registry.lock.RUnlock()
	return registry.applicationLoader
}

// ApplicationLoader 定义项目级 app 文件的延迟装配入口。
// 加载器在配置完成后、AppInit 之前执行，对应 ThinkPHP App.load 的时序。
type ApplicationLoader func(app *App) error

// RouteLoader 定义可返回初始化错误的应用路由加载器。
type RouteLoader func(app *App) error

// RegisterApplicationLoader 注册兼容模式下的项目定义入口。
// 生成代码只声明该入口，真正装配由 Initialize 在正确生命周期阶段执行。
func (app *App) RegisterApplicationLoader(loader ApplicationLoader) error {
	if app == nil {
		return ErrNilApplication
	}
	app.registrationMu.RLock()
	defer app.registrationMu.RUnlock()
	if err := app.ensureRegistrationOpen(); err != nil {
		return err
	}
	return app.registry.registerApplicationLoader(loader)
}

// RegisterController 在当前应用实例中注册控制器类型。
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

// RegisterModel 注册由 service:discover 自动找到的业务模型类型。
// 开发者只需把模型放在 app/<应用名>/model，无需维护额外注册表。
// 嵌入 *db.Model 的类型在应用装配阶段建立独立实例工厂，其余类型保留元数据预热用途。
func (app *App) RegisterModel(name string, model interface{}) error {
	if app == nil {
		return ErrNilApplication
	}
	app.registrationMu.RLock()
	defer app.registrationMu.RUnlock()
	if err := app.ensureRegistrationOpen(); err != nil {
		return err
	}
	return app.registry.registerModel(name, model)
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

// RegisterApplicationMiddleware 注册 app/<name>/middleware.go 中声明的应用中间件。
// 它在全局中间件之后、路由分发之前执行，对应 ThinkPHP 的 app 管道。
func (app *App) RegisterApplicationMiddleware(handler middleware.Handler) error {
	if app == nil {
		return ErrNilApplication
	}
	app.registrationMu.RLock()
	defer app.registrationMu.RUnlock()
	if err := app.ensureRegistrationOpen(); err != nil {
		return err
	}
	return app.registry.registerApplicationMiddleware(handler)
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
	if app.lifecycle.state >= ApplicationStateInitialized {
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

// RegisteredModelTypes 返回按模型名称稳定排序的类型快照，供模型校验命令使用。
func (app *App) RegisteredModelTypes() []reflect.Type {
	if app == nil {
		return nil
	}
	models := app.registry.snapshotModels()
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]reflect.Type, 0, len(names))
	for _, name := range names {
		result = append(result, models[name])
	}
	return result
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

func (app *App) snapshotApplicationMiddlewareRegistry() []middleware.Handler {
	if app == nil {
		return nil
	}
	return app.registry.snapshotApplicationMiddlewares()
}

func (app *App) loadApplicationDefinition() error {
	if app == nil {
		return ErrNilApplication
	}
	loader := app.registry.snapshotApplicationLoader()
	if loader == nil {
		return nil
	}
	return safeLoadApplication(loader, app)
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

func safeLoadApplication(loader ApplicationLoader, app *App) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", ErrRegistrationCallbackPanic, recovered)
		}
	}()
	return loader(app)
}
