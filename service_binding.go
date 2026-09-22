package framework

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	frameworkContext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/exception"
	frameworkLog "github.com/zhuhanxin0308/thinkgo/framework/log"
)

// ServiceName 是应用容器中服务绑定的稳定名称。
//
// 内置服务应优先使用本文件提供的常量；自定义 Provider 可以使用自定义名称，
// 但仍应通过 ResolveService 或 ResolveServiceAs 访问，避免业务代码直接依赖容器细节。
type ServiceName string

// 内置服务名称构成应用对外提供的稳定服务访问边界。
const (
	ServiceApp             ServiceName = "app"
	ServiceContainer       ServiceName = "container"
	ServiceConfig          ServiceName = "config"
	ServiceEnv             ServiceName = "env"
	ServiceEvent           ServiceName = "event"
	ServiceRoute           ServiceName = "route"
	ServiceMiddleware      ServiceName = "middleware"
	ServiceMetrics         ServiceName = "metrics"
	ServiceHealth          ServiceName = "health"
	ServiceDebug           ServiceName = "debug"
	ServiceLog             ServiceName = "log"
	ServiceLang            ServiceName = "lang"
	ServiceCache           ServiceName = "cache"
	ServiceFilesystem      ServiceName = "filesystem"
	ServiceCookie          ServiceName = "cookie"
	ServiceSession         ServiceName = "session"
	ServiceDB              ServiceName = "db"
	ServiceDBManager       ServiceName = "db_manager"
	ServiceView            ServiceName = "view"
	ServiceMigration       ServiceName = "migration"
	ServiceTelemetry       ServiceName = "telemetry"
	ServiceRequest         ServiceName = "request"
	ServiceExceptionHandle ServiceName = "exception_handle"
)

var (
	// ErrInvalidServiceName 表示服务名称为空或包含名称边界空白。
	ErrInvalidServiceName = errors.New("服务名称无效")
	// ErrServiceNotReady 表示服务所属应用尚未完成可用状态的装配。
	ErrServiceNotReady = errors.New("服务尚未就绪")
	// ErrServiceNotBound 表示应用已进入运行阶段，但目标服务没有绑定。
	ErrServiceNotBound = errors.New("服务尚未绑定")
	// ErrServiceUnavailable 表示服务已有绑定，但实例解析或实例状态不可用。
	ErrServiceUnavailable = errors.New("服务不可用")
	// ErrServiceTypeMismatch 表示服务绑定实例与调用方期望的类型不一致。
	ErrServiceTypeMismatch = errors.New("服务类型不匹配")
)

const (
	serviceKeyApp             = string(ServiceApp)
	serviceKeyContainer       = string(ServiceContainer)
	serviceKeyConfig          = string(ServiceConfig)
	serviceKeyEnv             = string(ServiceEnv)
	serviceKeyEvent           = string(ServiceEvent)
	serviceKeyRoute           = string(ServiceRoute)
	serviceKeyMiddleware      = string(ServiceMiddleware)
	serviceKeyMetrics         = string(ServiceMetrics)
	serviceKeyHealth          = string(ServiceHealth)
	serviceKeyDebug           = string(ServiceDebug)
	serviceKeyLog             = string(ServiceLog)
	serviceKeyLang            = string(ServiceLang)
	serviceKeyCache           = string(ServiceCache)
	serviceKeyFilesystem      = string(ServiceFilesystem)
	serviceKeyCookie          = string(ServiceCookie)
	serviceKeySession         = string(ServiceSession)
	serviceKeyDB              = string(ServiceDB)
	serviceKeyDBManager       = string(ServiceDBManager)
	serviceKeyView            = string(ServiceView)
	serviceKeyMigration       = string(ServiceMigration)
	serviceKeyTelemetry       = string(ServiceTelemetry)
	serviceKeyRequest         = string(ServiceRequest)
	serviceKeyExceptionHandle = string(ServiceExceptionHandle)
)

// ResolveService 通过应用容器解析服务，并把未就绪、未绑定和解析失败统一转换为状态错误。
//
// 基础服务在构造阶段即可解析；缓存、Session、数据库等依赖配置的服务，
// 必须等 Initialize 成功后再解析。
func (app *App) ResolveService(name ServiceName) (interface{}, error) {
	return app.resolveService(name, nil)
}

// resolveService 统一应用解析入口；作用域容器仍保留自身的生命周期和依赖链错误。
func (app *App) resolveService(name ServiceName, ctx context.Context, params ...interface{}) (interface{}, error) {
	if app == nil {
		return nil, ErrNilApplication
	}
	if err := validateServiceName(name); err != nil {
		return nil, err
	}

	state := app.State()
	if state == ApplicationStateClosing || state == ApplicationStateClosed {
		return nil, fmt.Errorf("%w: 服务 %q 当前状态为 %s", ErrApplicationClosed, name, state)
	}
	if app.container == nil {
		return nil, fmt.Errorf("%w: 服务 %q 缺少应用容器", ErrServiceUnavailable, name)
	}

	key := string(name)
	if !app.Has(key) {
		if state == ApplicationStateConstructed || state == ApplicationStateInitializing || state == ApplicationStateFailed {
			return nil, fmt.Errorf("%w: 服务 %q 当前状态为 %s", ErrServiceNotReady, name, state)
		}
		return nil, fmt.Errorf("%w: 服务 %q 当前状态为 %s", ErrServiceNotBound, name, state)
	}

	var instance interface{}
	var err error
	if ctx == nil {
		instance, err = app.container.Make(key, params...)
	} else {
		instance, err = app.container.MakeContext(ctx, key, params...)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: 服务 %q 在状态 %s 下解析失败: %w", ErrServiceUnavailable, name, state, err)
	}
	if isNilServiceInstance(instance) {
		return nil, fmt.Errorf("%w: 服务 %q 在状态 %s 下返回空实例", ErrServiceUnavailable, name, state)
	}
	return instance, nil
}

// ResolveServiceAs 通过泛型完成服务解析和类型校验，避免业务代码散落类型断言。
func ResolveServiceAs[T any](app *App, name ServiceName) (T, error) {
	var zero T
	instance, err := app.ResolveService(name)
	if err != nil {
		return zero, err
	}

	service, ok := instance.(T)
	if !ok {
		expectedType := reflect.TypeOf((*T)(nil)).Elem()
		return zero, fmt.Errorf("%w: 服务 %q 期望类型 %s，实际类型 %T", ErrServiceTypeMismatch, name, expectedType, instance)
	}
	return service, nil
}

func validateServiceName(name ServiceName) error {
	value := string(name)
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %q", ErrInvalidServiceName, name)
	}
	return nil
}

func isNilServiceInstance(instance interface{}) bool {
	if instance == nil {
		return true
	}
	value := reflect.ValueOf(instance)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func wrapServiceBindingError(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("绑定服务 %q 失败: %w", name, err)
}

// bindFoundationServices 将构造阶段可用的基础服务绑定到容器。
// 依赖配置的运行时服务由对应 Provider 在 Initialize 阶段绑定。
func (app *App) bindFoundationServices() error {
	bindings := []struct {
		name     string
		instance interface{}
	}{
		{name: serviceKeyApp, instance: app},
		{name: serviceKeyContainer, instance: app.container},
		{name: serviceKeyConfig, instance: app.config},
		{name: serviceKeyEnv, instance: app.env},
		{name: serviceKeyEvent, instance: app.event},
		{name: serviceKeyRoute, instance: app.route},
		{name: serviceKeyMiddleware, instance: app.middleware},
		{name: serviceKeyMetrics, instance: app.metrics},
		{name: serviceKeyHealth, instance: app.health},
		{name: serviceKeyDebug, instance: app.debug},
		{name: serviceKeyMigration, instance: app.migrations},
		{name: serviceKeyTelemetry, instance: app.telemetry},
	}
	var bindErr error
	for _, binding := range bindings {
		if err := app.Instance(binding.name, binding.instance); err != nil {
			bindErr = errors.Join(bindErr, fmt.Errorf("绑定基础服务 %q 失败: %w", binding.name, err))
		}
	}
	// Request 必须在每次 HTTP 请求中重新创建；异常处理器按应用单例解析，
	// 二者都可以在 app/provider.go 中被项目绑定覆盖。
	if err := app.BindFactory(serviceKeyRequest, frameworkContext.NewRequest); err != nil {
		bindErr = errors.Join(bindErr, wrapServiceBindingError(serviceKeyRequest, err))
	}
	if err := app.Bind(serviceKeyExceptionHandle, newDefaultExceptionHandler); err != nil {
		bindErr = errors.Join(bindErr, wrapServiceBindingError(serviceKeyExceptionHandle, err))
	}
	return bindErr
}

// newDefaultExceptionHandler 在日志 Provider 就绪后创建默认异常处理器。
func newDefaultExceptionHandler(container *Container) (exception.Handler, error) {
	applicationValue, err := container.Make(serviceKeyApp)
	if err != nil {
		return nil, fmt.Errorf("解析异常处理器应用失败: %w", err)
	}
	application, ok := applicationValue.(*App)
	if !ok || application == nil {
		return nil, fmt.Errorf("异常处理器应用类型错误: %T", applicationValue)
	}
	logValue, err := container.Make(serviceKeyLog)
	if err != nil {
		return nil, fmt.Errorf("解析异常处理器日志失败: %w", err)
	}
	logger, ok := logValue.(*frameworkLog.Log)
	if !ok || logger == nil {
		return nil, fmt.Errorf("异常处理器日志类型错误: %T", logValue)
	}
	return &exception.Handle{
		App:    application,
		Log:    logger,
		TplDir: application.ExceptionTemplatePath(),
	}, nil
}
