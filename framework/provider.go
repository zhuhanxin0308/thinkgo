package framework

import (
	"errors"
	"fmt"
	"reflect"
	"sync"

	"thinkgo/framework/event"
)

var (
	// ErrInvalidProvider 表示 Provider 为空或不是非空指针。
	ErrInvalidProvider = errors.New("无效服务提供者")
	// ErrDuplicateProvider 表示同一 Provider 实例已注册。
	ErrDuplicateProvider = errors.New("服务提供者重复注册")
	// ErrProviderRegistrationClosed 表示应用已开始启动或关闭，不再接受新 Provider。
	ErrProviderRegistrationClosed = errors.New("服务提供者注册阶段已关闭")
	// ErrProviderLifecycleClosed 表示 Provider 生命周期已经关闭。
	ErrProviderLifecycleClosed = errors.New("服务提供者生命周期已关闭")
	// ErrProviderCallbackPanic 表示 Provider 回调 panic 已转换为错误。
	ErrProviderCallbackPanic = errors.New("服务提供者回调发生 panic")
)

// ServiceProvider 定义可失败的注册与启动阶段。
type ServiceProvider interface {
	// Register 只绑定服务和注册监听器，不执行依赖其它 Provider 的启动逻辑。
	Register(app *App) error
	// Boot 在全部成功 Register 后按注册顺序执行。
	Boot(app *App) error
}

// ServiceProviderShutdown 是可选的资源释放阶段，按注册逆序执行。
type ServiceProviderShutdown interface {
	Shutdown(app *App) error
}

type providerIdentity struct {
	typeName string
	pointer  uintptr
}

type providerRegistration struct {
	provider   ServiceProvider
	registered bool
}

type providerLifecycle struct {
	lock               sync.Mutex
	condition          *sync.Cond
	providers          []*providerRegistration
	identities         map[providerIdentity]struct{}
	registering        int
	registrationClosed bool
	booting            bool
	booted             bool
	closed             bool
	bootErr            error
}

// RegisterProvider 完成单个 Provider 的 Register；错误会进入应用启动错误。
func (app *App) RegisterProvider(provider ServiceProvider) error {
	if app == nil {
		return ErrNilApplication
	}
	identity, err := identifyProvider(provider)
	if err != nil {
		app.recordStartupError(err)
		return err
	}
	lifecycle := &app.providers
	lifecycle.lock.Lock()
	lifecycle.initializeLocked()
	if lifecycle.registrationClosed || lifecycle.closed {
		lifecycle.lock.Unlock()
		return ErrProviderRegistrationClosed
	}
	if _, exists := lifecycle.identities[identity]; exists {
		lifecycle.lock.Unlock()
		return ErrDuplicateProvider
	}
	lifecycle.identities[identity] = struct{}{}
	registration := &providerRegistration{provider: provider}
	lifecycle.providers = append(lifecycle.providers, registration)
	lifecycle.registering++
	lifecycle.lock.Unlock()

	registerErr := safeProviderRegister(provider, app)
	lifecycle.lock.Lock()
	lifecycle.registering--
	if registerErr != nil {
		delete(lifecycle.identities, identity)
		lifecycle.removeRegistrationLocked(registration)
	} else {
		registration.registered = true
	}
	lifecycle.condition.Broadcast()
	lifecycle.lock.Unlock()
	if registerErr != nil {
		wrapped := fmt.Errorf("服务提供者 %T 注册失败: %w", provider, registerErr)
		app.recordStartupError(wrapped)
		return wrapped
	}
	return nil
}

// BootProviders 关闭注册阶段，等待在途 Register，并按确定顺序启动全部 Provider。
func (app *App) BootProviders() error {
	if app == nil {
		return ErrNilApplication
	}
	lifecycle := &app.providers
	lifecycle.lock.Lock()
	lifecycle.initializeLocked()
	for lifecycle.booting {
		lifecycle.condition.Wait()
	}
	if lifecycle.booted {
		err := lifecycle.bootErr
		lifecycle.lock.Unlock()
		return err
	}
	if lifecycle.closed {
		lifecycle.lock.Unlock()
		return ErrProviderLifecycleClosed
	}
	lifecycle.registrationClosed = true
	lifecycle.booting = true
	for lifecycle.registering > 0 {
		lifecycle.condition.Wait()
	}
	providers := lifecycle.snapshotProvidersLocked()
	lifecycle.lock.Unlock()

	bootErr := app.dispatchLifecycleEvent(event.NewAppInitEvent())
	if bootErr == nil {
		for _, provider := range providers {
			if err := safeProviderBoot(provider, app); err != nil {
				bootErr = fmt.Errorf("服务提供者 %T 启动失败: %w", provider, err)
				break
			}
		}
	}
	if bootErr == nil {
		bootErr = app.dispatchLifecycleEvent(event.NewRouteLoadedEvent())
	}

	lifecycle.lock.Lock()
	lifecycle.booting = false
	lifecycle.booted = true
	lifecycle.bootErr = bootErr
	lifecycle.condition.Broadcast()
	lifecycle.lock.Unlock()
	if bootErr != nil {
		app.recordStartupError(bootErr)
	}
	return bootErr
}

func (lifecycle *providerLifecycle) initializeLocked() {
	if lifecycle.condition == nil {
		lifecycle.condition = sync.NewCond(&lifecycle.lock)
	}
	if lifecycle.identities == nil {
		lifecycle.identities = make(map[providerIdentity]struct{})
	}
}

func (lifecycle *providerLifecycle) snapshotProvidersLocked() []ServiceProvider {
	providers := make([]ServiceProvider, 0, len(lifecycle.providers))
	for _, registration := range lifecycle.providers {
		if registration.registered {
			providers = append(providers, registration.provider)
		}
	}
	return providers
}

func (lifecycle *providerLifecycle) removeRegistrationLocked(target *providerRegistration) {
	for index, registration := range lifecycle.providers {
		if registration != target {
			continue
		}
		copy(lifecycle.providers[index:], lifecycle.providers[index+1:])
		last := len(lifecycle.providers) - 1
		lifecycle.providers[last] = nil
		lifecycle.providers = lifecycle.providers[:last]
		return
	}
}

func (app *App) dispatchLifecycleEvent(currentEvent event.Event) error {
	if app.Event == nil {
		return nil
	}
	if err := app.Event.Dispatch(currentEvent); err != nil {
		return fmt.Errorf("分发生命周期事件 %q 失败: %w", currentEvent.Name(), err)
	}
	return nil
}

func (app *App) shutdownProviders() error {
	lifecycle := &app.providers
	lifecycle.lock.Lock()
	lifecycle.initializeLocked()
	lifecycle.registrationClosed = true
	lifecycle.closed = true
	for lifecycle.registering > 0 || lifecycle.booting {
		lifecycle.condition.Wait()
	}
	providers := lifecycle.snapshotProvidersLocked()
	lifecycle.lock.Unlock()

	shutdownErrors := make([]error, 0)
	for index := len(providers) - 1; index >= 0; index-- {
		shutdownProvider, ok := providers[index].(ServiceProviderShutdown)
		if !ok {
			continue
		}
		if err := safeProviderShutdown(shutdownProvider, app); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("服务提供者 %T 关闭失败: %w", providers[index], err))
		}
	}
	return errors.Join(shutdownErrors...)
}

func identifyProvider(provider ServiceProvider) (providerIdentity, error) {
	if provider == nil {
		return providerIdentity{}, fmt.Errorf("%w: 服务提供者不能为空", ErrInvalidProvider)
	}
	value := reflect.ValueOf(provider)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return providerIdentity{}, fmt.Errorf("%w: 服务提供者必须是非空指针", ErrInvalidProvider)
	}
	return providerIdentity{typeName: value.Type().String(), pointer: value.Pointer()}, nil
}

func safeProviderRegister(provider ServiceProvider, app *App) (err error) {
	defer recoverProviderPanic("Register", &err)
	return provider.Register(app)
}

func safeProviderBoot(provider ServiceProvider, app *App) (err error) {
	defer recoverProviderPanic("Boot", &err)
	return provider.Boot(app)
}

func safeProviderShutdown(provider ServiceProviderShutdown, app *App) (err error) {
	defer recoverProviderPanic("Shutdown", &err)
	return provider.Shutdown(app)
}

func recoverProviderPanic(stage string, target *error) {
	if recovered := recover(); recovered != nil {
		*target = fmt.Errorf("%w: %s: %v", ErrProviderCallbackPanic, stage, recovered)
	}
}
