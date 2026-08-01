package framework

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ServiceName 是应用容器中服务绑定的稳定名称。
//
// 内置服务应优先使用本文件提供的常量；自定义 Provider 可以使用自定义名称，
// 但仍应通过 ResolveService 或 ResolveServiceAs 访问，避免业务代码直接依赖容器细节。
type ServiceName string

// 内置服务名称构成应用对外提供的稳定服务访问边界。
const (
	ServiceApp        ServiceName = "app"
	ServiceContainer  ServiceName = "container"
	ServiceConfig     ServiceName = "config"
	ServiceEnv        ServiceName = "env"
	ServiceEvent      ServiceName = "event"
	ServiceRoute      ServiceName = "route"
	ServiceMiddleware ServiceName = "middleware"
	ServiceMetrics    ServiceName = "metrics"
	ServiceHealth     ServiceName = "health"
	ServiceDebug      ServiceName = "debug"
	ServiceLog        ServiceName = "log"
	ServiceLang       ServiceName = "lang"
	ServiceCache      ServiceName = "cache"
	ServiceCookie     ServiceName = "cookie"
	ServiceSession    ServiceName = "session"
	ServiceDB         ServiceName = "db"
	ServiceDBManager  ServiceName = "db_manager"
	ServiceView       ServiceName = "view"
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
	serviceKeyApp        = string(ServiceApp)
	serviceKeyContainer  = string(ServiceContainer)
	serviceKeyConfig     = string(ServiceConfig)
	serviceKeyEnv        = string(ServiceEnv)
	serviceKeyEvent      = string(ServiceEvent)
	serviceKeyRoute      = string(ServiceRoute)
	serviceKeyMiddleware = string(ServiceMiddleware)
	serviceKeyMetrics    = string(ServiceMetrics)
	serviceKeyHealth     = string(ServiceHealth)
	serviceKeyDebug      = string(ServiceDebug)
	serviceKeyLog        = string(ServiceLog)
	serviceKeyLang       = string(ServiceLang)
	serviceKeyCache      = string(ServiceCache)
	serviceKeyCookie     = string(ServiceCookie)
	serviceKeySession    = string(ServiceSession)
	serviceKeyDB         = string(ServiceDB)
	serviceKeyDBManager  = string(ServiceDBManager)
	serviceKeyView       = string(ServiceView)
)

// ResolveService 通过应用容器解析服务，并把未就绪、未绑定和解析失败统一转换为状态错误。
//
// 基础服务在构造阶段即可解析；缓存、Session、数据库等依赖配置的服务，
// 必须等 Initialize 成功后再解析。
func (app *App) ResolveService(name ServiceName) (interface{}, error) {
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

	instance, err := app.Make(key)
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

// bindFoundationServices 将构造阶段可用的基础服务绑定到容器。
// 依赖配置的运行时服务由对应 Provider 在 Initialize 阶段绑定。
func (app *App) bindFoundationServices() {
	app.Instance(serviceKeyApp, app)
	app.Instance(serviceKeyContainer, app.container)
	app.Instance(serviceKeyConfig, app.config)
	app.Instance(serviceKeyEnv, app.env)
	app.Instance(serviceKeyEvent, app.event)
	app.Instance(serviceKeyRoute, app.route)
	app.Instance(serviceKeyMiddleware, app.middleware)
	app.Instance(serviceKeyMetrics, app.metrics)
	app.Instance(serviceKeyHealth, app.health)
	app.Instance(serviceKeyDebug, app.debug)
}
