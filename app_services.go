package framework

import (
	"fmt"
	"reflect"

	"github.com/zhuhanxin0308/thinkgo/framework/cache"
	"github.com/zhuhanxin0308/thinkgo/framework/config"
	"github.com/zhuhanxin0308/thinkgo/framework/cookie"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/debug"
	"github.com/zhuhanxin0308/thinkgo/framework/env"
	"github.com/zhuhanxin0308/thinkgo/framework/event"
	"github.com/zhuhanxin0308/thinkgo/framework/filesystem"
	"github.com/zhuhanxin0308/thinkgo/framework/health"
	"github.com/zhuhanxin0308/thinkgo/framework/lang"
	"github.com/zhuhanxin0308/thinkgo/framework/log"
	"github.com/zhuhanxin0308/thinkgo/framework/metrics"
	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
	"github.com/zhuhanxin0308/thinkgo/framework/migration"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
	"github.com/zhuhanxin0308/thinkgo/framework/session"
	"github.com/zhuhanxin0308/thinkgo/framework/telemetry"
	"github.com/zhuhanxin0308/thinkgo/framework/view"
)

// Bind 将服务绑定到应用内部容器，并在生命周期不允许修改时返回明确错误。
func (app *App) Bind(abstract string, concrete interface{}) error {
	container, err := app.ownedContainer()
	if err != nil {
		return err
	}
	return container.TryBind(abstract, concrete)
}

// BindFactory 将工厂服务绑定到应用内部容器。
func (app *App) BindFactory(abstract string, concrete interface{}) error {
	container, err := app.ownedContainer()
	if err != nil {
		return err
	}
	return container.TryBindFactory(abstract, concrete)
}

// BindScoped 将服务绑定为每个请求或任务作用域只创建一次。
func (app *App) BindScoped(abstract string, concrete interface{}) error {
	container, err := app.ownedContainer()
	if err != nil {
		return err
	}
	return container.TryBindScoped(abstract, concrete)
}

// NewScope 创建共享应用绑定但隔离 Scoped 服务的显式作用域。
func (app *App) NewScope() (*ContainerScope, error) {
	if err := app.serviceAccessError(); err != nil {
		return nil, err
	}
	return app.container.NewScope(), nil
}

// Instance 将已创建的实例绑定到应用内部容器。
func (app *App) Instance(abstract string, instance interface{}) error {
	container, err := app.ownedContainer()
	if err != nil {
		return err
	}
	return container.TryInstance(abstract, instance)
}

// Make 是兼容字符串服务名的解析入口，沿用 ResolveService 的状态错误语义。
func (app *App) Make(abstract string, params ...interface{}) (interface{}, error) {
	return app.resolveService(ServiceName(abstract), nil, params...)
}

// Get 从应用内部容器解析必需服务；解析失败时抛出 panic。
func (app *App) Get(abstract string) interface{} {
	instance, err := app.Make(abstract)
	if err != nil {
		panic(err)
	}
	return instance
}

// Has 判断应用内部容器是否存在指定服务绑定；关闭阶段统一返回 false。
func (app *App) Has(abstract string) bool {
	return app != nil && app.serviceAccessError() == nil && app.container.Has(abstract)
}

// Delete 删除应用内部容器中的服务绑定。
func (app *App) Delete(abstract string) error {
	container, err := app.ownedContainer()
	if err != nil {
		return err
	}
	return container.TryDelete(abstract)
}

// applyContainerMutation 在应用进入运行态后冻结容器和服务快照，避免公开容器指针绕过边界。
func (app *App) applyContainerMutation(container *Container, mutation containerMutation) error {
	if app == nil {
		return ErrNilApplication
	}
	app.serviceMutationMu.Lock()
	defer app.serviceMutationMu.Unlock()
	if err := app.containerMutationTargetError(container); err != nil {
		return err
	}
	if err := app.validateContainerMutation(mutation); err != nil {
		return err
	}
	if mutation.typeName == containerMutationInstance {
		if err := app.resources.track(mutation.abstract, mutation.concrete); err != nil {
			return err
		}
	}
	container.applyMutationDirect(mutation)
	if mutation.typeName == containerMutationInstance {
		app.syncServiceField(mutation.abstract, mutation.concrete)
	}
	return nil
}

// containerMutationTargetError 统一单次和批量变更的生命周期门禁；调用方持有服务变更锁。
func (app *App) containerMutationTargetError(container *Container) error {
	app.lifecycle.lock.Lock()
	state := app.lifecycle.state
	closed := app.lifecycle.closed
	app.lifecycle.lock.Unlock()
	if closed || state == ApplicationStateClosing || state == ApplicationStateClosed {
		return fmt.Errorf("%w: 当前状态为 %s", ErrApplicationClosed, state)
	}
	if state == ApplicationStateRunning {
		return fmt.Errorf("%w: 服务容器已经冻结", ErrApplicationRunning)
	}
	if app.container == nil {
		return fmt.Errorf("%w: 应用容器未初始化", ErrServiceUnavailable)
	}
	if container == nil || container.sharedState() != app.container.sharedState() {
		return ErrContainerOwnershipConflict
	}
	return nil
}

// ownedContainer 确保手工构造的历史 App 也能把容器接入统一生命周期门禁。
func (app *App) ownedContainer() (*Container, error) {
	if app == nil {
		return nil, ErrNilApplication
	}
	if app.container == nil {
		return nil, fmt.Errorf("%w: 应用容器未初始化", ErrServiceUnavailable)
	}
	if err := app.container.attachApplication(app); err != nil {
		return nil, err
	}
	return app.container, nil
}

// bindDeferredManagedService 仅供框架延迟装配自身管理的内置服务。
// 普通调用方必须使用 Instance，确保容器实例与 App 字段快照同步。
func (app *App) bindDeferredManagedService(abstract string, concrete interface{}) error {
	container, err := app.ownedContainer()
	if err != nil {
		return err
	}
	return app.applyContainerMutation(container, containerMutation{
		typeName:  containerMutationBind,
		abstract:  abstract,
		concrete:  concrete,
		lifecycle: Singleton,
		internal:  true,
	})
}

func (app *App) validateContainerMutation(mutation containerMutation) error {
	expectedType, managed := managedServiceType(mutation.abstract)
	if !managed {
		return nil
	}
	if mutation.typeName == containerMutationBind {
		if mutation.internal && mutation.abstract == serviceKeyDB && mutation.lifecycle == Singleton {
			return nil
		}
		return fmt.Errorf("%w: 服务 %q 必须通过 Instance 同步替换", ErrProtectedServiceMutation, mutation.abstract)
	}
	if mutation.typeName == containerMutationDelete {
		return fmt.Errorf("%w: 服务 %q 不能从应用容器删除", ErrProtectedServiceMutation, mutation.abstract)
	}
	if mutation.typeName != containerMutationInstance {
		return fmt.Errorf("%w: 服务 %q 的变更类型无效", ErrProtectedServiceMutation, mutation.abstract)
	}
	if mutation.abstract == serviceKeyApp {
		if mutation.concrete != app {
			return fmt.Errorf("%w: 服务 %q 必须保持当前应用身份", ErrProtectedServiceMutation, mutation.abstract)
		}
		return nil
	}
	if mutation.abstract == serviceKeyContainer {
		if mutation.concrete != app.container {
			return fmt.Errorf("%w: 服务 %q 必须保持当前容器身份", ErrProtectedServiceMutation, mutation.abstract)
		}
		return nil
	}
	if isNilServiceInstance(mutation.concrete) {
		return fmt.Errorf("%w: 内置服务 %q 不能为空", ErrServiceUnavailable, mutation.abstract)
	}
	actualType := reflect.TypeOf(mutation.concrete)
	if actualType != expectedType {
		return fmt.Errorf(
			"%w: 服务 %q 期望类型 %s，实际类型 %s",
			ErrServiceTypeMismatch,
			mutation.abstract,
			expectedType,
			actualType,
		)
	}
	return nil
}

// managedServiceType 返回需要与 App 内部字段保持同一实例的内置服务类型。
func managedServiceType(abstract string) (reflect.Type, bool) {
	definition, exists := managedServices[abstract]
	return definition.typ, exists
}

type managedServiceDefinition struct {
	typ      reflect.Type
	snapshot func(*App, any)
}

func defineManagedService[T any](snapshot func(*App, T)) managedServiceDefinition {
	return managedServiceDefinition{typ: reflect.TypeFor[T](), snapshot: func(app *App, instance any) {
		if value, ok := instance.(T); ok {
			snapshot(app, value)
		}
	}}
}

// managedServices 在同一处声明类型和内部快照更新规则，资源关闭由所有权登记统一管理。
var managedServices = map[string]managedServiceDefinition{
	serviceKeyApp:        defineManagedService(func(*App, *App) {}),
	serviceKeyContainer:  defineManagedService(func(app *App, value *Container) { app.container = value }),
	serviceKeyConfig:     defineManagedService(func(app *App, value *config.Config) { app.config = value }),
	serviceKeyEnv:        defineManagedService(func(app *App, value *env.Env) { app.env = value }),
	serviceKeyEvent:      defineManagedService(func(app *App, value *event.Dispatcher) { app.event = value }),
	serviceKeyRoute:      defineManagedService(func(app *App, value *route.Router) { app.route = value; app.routeFacade = newRouteFacade(app, value) }),
	serviceKeyMiddleware: defineManagedService(func(app *App, value *middleware.Pipeline) { app.middleware = value }),
	serviceKeyMetrics:    defineManagedService(func(app *App, value *metrics.Registry) { app.metrics = value }),
	serviceKeyHealth:     defineManagedService(func(app *App, value *health.Registry) { app.health = value }),
	serviceKeyDebug:      defineManagedService(func(app *App, value *debug.Debug) { app.debug = value }),
	serviceKeyLog:        defineManagedService(func(app *App, value *log.Log) { app.log = value }),
	serviceKeyLang:       defineManagedService(func(app *App, value *lang.Lang) { app.lang = value }),
	serviceKeyCache:      defineManagedService(func(app *App, value *cache.Cache) { app.cache = value }),
	serviceKeyFilesystem: defineManagedService(func(app *App, value *filesystem.Filesystem) { app.filesystem = value }),
	serviceKeyCookie:     defineManagedService(func(app *App, value *cookie.Cookie) { app.cookie = value }),
	serviceKeySession:    defineManagedService(func(app *App, value *session.Session) { app.session = value }),
	serviceKeyDB:         defineManagedService(func(app *App, value *db.DB) { app.db = value }),
	serviceKeyDBManager:  defineManagedService(func(app *App, value *db.Manager) { app.dbManager = value }),
	serviceKeyView:       defineManagedService(func(app *App, value *view.View) { app.view = value }),
	serviceKeyMigration:  defineManagedService(func(app *App, value *migration.Registry) { app.migrations = value }),
	serviceKeyTelemetry:  defineManagedService(func(app *App, value *telemetry.Tracing) { app.telemetry = value }),
}

func (app *App) serviceAccessError() error {
	if app == nil {
		return ErrNilApplication
	}
	app.lifecycle.lock.Lock()
	state := app.lifecycle.state
	closed := app.lifecycle.closed
	app.lifecycle.lock.Unlock()
	if closed || state == ApplicationStateClosing || state == ApplicationStateClosed {
		return fmt.Errorf("%w: 当前状态为 %s", ErrApplicationClosed, state)
	}
	if app.container == nil {
		return fmt.Errorf("%w: 应用容器未初始化", ErrServiceUnavailable)
	}
	return nil
}

// syncServiceField 同步内置服务的内部快照，保证框架内部装配逻辑与容器保持一致。
// 业务代码应通过 ResolveService 或 ResolveServiceAs 访问服务，不应依赖这些内部字段。
func (app *App) syncServiceField(abstract string, instance interface{}) {
	if app == nil {
		return
	}
	if definition, exists := managedServices[abstract]; exists {
		definition.snapshot(app, instance)
	}
}
