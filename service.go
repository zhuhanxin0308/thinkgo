package framework

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ErrApplicationServiceRegistrationInProgress 表示同类型服务仍在注册或替换。
var ErrApplicationServiceRegistrationInProgress = errors.New("应用服务正在注册")

// Service 是应用服务基类，对应 ThinkPHP 的 think\Service。
// 业务服务嵌入该类型后即可在无参数 Register、Boot 中取得当前 App。
type Service struct {
	application *App
}

// App 返回当前服务所属应用。
func (service *Service) App() *App {
	if service == nil {
		return nil
	}
	return service.application
}

func (service *Service) bindApplication(application *App) {
	if service != nil {
		service.application = application
	}
}

type serviceApplicationBinder interface {
	bindApplication(*App)
}

type serviceRegister interface {
	Register() error
}

type serviceRegisterWithoutError interface {
	Register()
}

type serviceInitialize interface {
	Initialize() error
}

type serviceInitializeWithoutError interface {
	Initialize()
}

type serviceBoot interface {
	Boot() error
}

type serviceBootWithoutError interface {
	Boot()
}

type serviceShutdown interface {
	Shutdown() error
}

type serviceShutdownWithoutError interface {
	Shutdown()
}

type applicationServiceProvider struct {
	service interface{}
}

// Register 注册 ThinkPHP 风格应用服务。force 允许在 Boot 前替换同类型实例。
// 已进入初始化阶段的旧实例仍在候选实例初始化成功后才被替换。
// 首次初始化与替换争用生命周期阶段时，可在该阶段结束后重试替换。
// 注册过程中由回调建立的资源由服务负责管理。
func (app *App) Register(service interface{}, force ...bool) error {
	if app == nil {
		return ErrNilApplication
	}
	if len(force) > 1 {
		return fmt.Errorf("Register 最多只能指定一个 force 参数")
	}
	if provider, ok := service.(ServiceProvider); ok {
		return app.RegisterProvider(provider)
	}
	if err := validateApplicationService(service); err != nil {
		return err
	}
	forceRegister := len(force) == 1 && force[0]
	serviceType := reflect.TypeOf(service)
	app.applicationServiceMu.Lock()
	if _, inFlight := app.applicationServiceInFlight[serviceType]; inFlight {
		app.applicationServiceMu.Unlock()
		return fmt.Errorf("%w: %T", ErrApplicationServiceRegistrationInProgress, service)
	}
	previous := findApplicationService(app.applicationServices, service)
	if previous != nil && !forceRegister {
		app.applicationServiceMu.Unlock()
		return nil
	}
	if app.applicationServiceInFlight == nil {
		app.applicationServiceInFlight = make(map[reflect.Type]struct{})
	}
	app.applicationServiceInFlight[serviceType] = struct{}{}
	app.applicationServiceMu.Unlock()
	defer func() {
		app.applicationServiceMu.Lock()
		delete(app.applicationServiceInFlight, serviceType)
		app.applicationServiceMu.Unlock()
	}()
	return app.registerApplicationServiceProvider(&applicationServiceProvider{service: service}, previous)
}

// GetService 返回首个同类型应用服务，对应 ThinkPHP App::getService。
// 参数可以是服务实例，也可以是 Go 完整类型名。
func (app *App) GetService(service interface{}) interface{} {
	if app == nil || service == nil {
		return nil
	}
	app.applicationServiceMu.RLock()
	registered := findApplicationService(app.applicationServices, service)
	app.applicationServiceMu.RUnlock()
	if registered == nil {
		return nil
	}
	return registered.service
}

func findApplicationService(services []*applicationServiceProvider, target interface{}) *applicationServiceProvider {
	targetType := reflect.TypeOf(target)
	name, byName := target.(string)
	name = strings.TrimSpace(name)
	for _, current := range services {
		serviceType := reflect.TypeOf(current.service)
		if serviceType == targetType || byName && serviceType.String() == name {
			return current
		}
	}
	return nil
}

// BootService 单独执行一个 ThinkPHP 风格服务的 Boot 回调。
func (app *App) BootService(service interface{}) error {
	if app == nil {
		return ErrNilApplication
	}
	if err := validateApplicationService(service); err != nil {
		return err
	}
	return safeProviderBoot(&applicationServiceProvider{service: service}, app)
}

func validateApplicationService(service interface{}) error {
	if service == nil {
		return fmt.Errorf("%w: 应用服务不能为空", ErrInvalidProvider)
	}
	value := reflect.ValueOf(service)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return fmt.Errorf("%w: 应用服务必须是非空指针", ErrInvalidProvider)
	}
	if _, ok := service.(serviceRegister); ok {
		return nil
	}
	if _, ok := service.(serviceRegisterWithoutError); ok {
		return nil
	}
	return fmt.Errorf("%w: 应用服务 %T 必须实现 Register() 或 Register() error", ErrInvalidProvider, service)
}

func (provider *applicationServiceProvider) Register(app *App) error {
	if provider == nil || provider.service == nil {
		return fmt.Errorf("%w: 应用服务不能为空", ErrInvalidProvider)
	}
	if binder, ok := provider.service.(serviceApplicationBinder); ok {
		binder.bindApplication(app)
	}
	if register, ok := provider.service.(serviceRegister); ok {
		return register.Register()
	}
	provider.service.(serviceRegisterWithoutError).Register()
	return nil
}

func (provider *applicationServiceProvider) Initialize(*App) error {
	if initialize, ok := provider.service.(serviceInitialize); ok {
		return initialize.Initialize()
	}
	if initialize, ok := provider.service.(serviceInitializeWithoutError); ok {
		initialize.Initialize()
	}
	return nil
}

func (provider *applicationServiceProvider) Boot(*App) error {
	if boot, ok := provider.service.(serviceBoot); ok {
		return boot.Boot()
	}
	if boot, ok := provider.service.(serviceBootWithoutError); ok {
		boot.Boot()
	}
	return nil
}

func (provider *applicationServiceProvider) Shutdown(*App) error {
	if shutdown, ok := provider.service.(serviceShutdown); ok {
		return shutdown.Shutdown()
	}
	if shutdown, ok := provider.service.(serviceShutdownWithoutError); ok {
		shutdown.Shutdown()
	}
	return nil
}
