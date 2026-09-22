package framework

import (
	"fmt"
	"reflect"
	"strings"
)

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

// Register 注册 ThinkPHP 风格应用服务；可选 force 参数强制重新注册同类型服务。
// 底层 Provider API 继续保留给框架扩展。
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
	app.applicationServiceMu.Lock()
	if !forceRegister {
		if registered := findApplicationService(app.applicationServices, service); registered != nil {
			app.applicationServiceMu.Unlock()
			return nil
		}
	}
	app.applicationServices = append(app.applicationServices, service)
	registrationIndex := len(app.applicationServices) - 1
	app.applicationServiceMu.Unlock()

	if err := app.RegisterProvider(&applicationServiceProvider{service: service}); err != nil {
		app.applicationServiceMu.Lock()
		if registrationIndex < len(app.applicationServices) && app.applicationServices[registrationIndex] == service {
			app.applicationServices = append(app.applicationServices[:registrationIndex], app.applicationServices[registrationIndex+1:]...)
		} else {
			for index := len(app.applicationServices) - 1; index >= 0; index-- {
				if app.applicationServices[index] == service {
					app.applicationServices = append(app.applicationServices[:index], app.applicationServices[index+1:]...)
					break
				}
			}
		}
		app.applicationServiceMu.Unlock()
		return err
	}
	return nil
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
	return registered
}

func findApplicationService(services []interface{}, target interface{}) interface{} {
	targetName := ""
	if name, ok := target.(string); ok {
		targetName = strings.TrimSpace(name)
	} else {
		targetType := reflect.TypeOf(target)
		if targetType == nil {
			return nil
		}
		targetName = targetType.String()
	}
	if targetName == "" {
		return nil
	}
	for _, service := range services {
		serviceType := reflect.TypeOf(service)
		if serviceType != nil && serviceType.String() == targetName {
			return service
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
