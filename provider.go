package framework

import (
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/zhuhanxin0308/thinkgo/framework/event"
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

// ServiceProviderInitializer 是可选的配置完成后初始化阶段。
//
// 该接口保持为可选接口，以兼容只有 Register 和 Boot 的现有 Provider。
// Initialize 执行时，应用配置和环境覆盖已经完成，但应用尚未进入 Boot 阶段。
type ServiceProviderInitializer interface {
	Initialize(app *App) error
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
	provider     ServiceProvider
	registered   bool
	initialized  bool
	initializing bool
}

type providerLifecycle struct {
	lock               sync.Mutex
	phaseMu            sync.Mutex
	condition          *sync.Cond
	providers          []*providerRegistration
	identities         map[providerIdentity]struct{}
	registering        int
	closing            bool
	registrationClosed bool
	booting            bool
	booted             bool
	closed             bool
	bootErr            error
	initializeOnce     sync.Once
	initializeErr      error
	initializing       bool
	initialized        bool
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
	if lifecycle.registrationClosed || lifecycle.closing || lifecycle.closed {
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
		// 没有 Initialize 阶段的 Provider 在 Register 完成后即可参与 Boot 和 Shutdown。
		if _, ok := provider.(ServiceProviderInitializer); !ok {
			registration.initialized = true
		}
	}
	lifecycle.condition.Broadcast()
	lifecycle.lock.Unlock()
	if registerErr != nil {
		wrapped := fmt.Errorf("服务提供者 %T 注册失败: %w", provider, registerErr)
		app.recordStartupError(wrapped)
		return wrapped
	}
	if lifecycle.providerWasInitialized(provider) {
		if initializeErr := app.initializeLateProvider(registration); initializeErr != nil {
			wrapped := fmt.Errorf("服务提供者 %T 初始化失败: %w", provider, initializeErr)
			app.recordStartupError(wrapped)
			return wrapped
		}
	}
	return nil
}

// initializeProviders 执行可选的 Provider 初始化阶段，并缓存结果避免重复装配。
func (app *App) initializeProviders() error {
	if app == nil {
		return ErrNilApplication
	}
	lifecycle := &app.providers
	lifecycle.lock.Lock()
	if (lifecycle.registrationClosed || lifecycle.closing) && !lifecycle.initialized {
		lifecycle.lock.Unlock()
		return ErrProviderLifecycleClosed
	}
	lifecycle.lock.Unlock()
	lifecycle.initializeOnce.Do(func() {
		lifecycle.phaseMu.Lock()
		defer lifecycle.phaseMu.Unlock()
		lifecycle.lock.Lock()
		lifecycle.initializeLocked()
		if lifecycle.closing || lifecycle.closed {
			lifecycle.initializeErr = ErrProviderLifecycleClosed
			lifecycle.lock.Unlock()
			return
		}
		lifecycle.initializing = true
		lifecycle.lock.Unlock()

		initializeErrors := app.initializePendingProviders()
		lifecycle.lock.Lock()
		lifecycle.initializeErr = initializeErrors
		lifecycle.initializing = false
		lifecycle.initialized = true
		lifecycle.condition.Broadcast()
		lifecycle.lock.Unlock()
	})
	lifecycle.lock.Lock()
	defer lifecycle.lock.Unlock()
	return lifecycle.initializeErr
}

func (lifecycle *providerLifecycle) providerWasInitialized(provider ServiceProvider) bool {
	lifecycle.lock.Lock()
	defer lifecycle.lock.Unlock()
	for _, registration := range lifecycle.providers {
		if registration.provider == provider {
			return lifecycle.initialized && !lifecycle.registrationClosed && !lifecycle.closing && !lifecycle.closed
		}
	}
	return false
}

// initializePendingProviders 在阶段锁内串行完成所有已注册但尚未尝试初始化的 Provider。
func (app *App) initializePendingProviders() error {
	lifecycle := &app.providers
	initializeErrors := make([]error, 0)
	for {
		lifecycle.lock.Lock()
		for lifecycle.registering > 0 {
			lifecycle.condition.Wait()
		}
		pending := make([]*providerRegistration, 0)
		for _, registration := range lifecycle.providers {
			if !registration.registered || registration.initialized || registration.initializing {
				continue
			}
			registration.initializing = true
			pending = append(pending, registration)
		}
		lifecycle.lock.Unlock()

		if len(pending) == 0 {
			return errors.Join(initializeErrors...)
		}
		for _, registration := range pending {
			var initializeErr error
			if _, ok := registration.provider.(ServiceProviderInitializer); ok {
				initializeErr = safeProviderInitialize(registration.provider, app)
			}
			lifecycle.lock.Lock()
			registration.initializing = false
			registration.initialized = true
			lifecycle.condition.Broadcast()
			lifecycle.lock.Unlock()
			if initializeErr != nil {
				initializeErrors = append(initializeErrors, fmt.Errorf("服务提供者 %T 初始化失败: %w", registration.provider, initializeErr))
			}
		}
	}
}

// initializeLateProvider 保证迟到 Provider 不会与 Boot 或 Shutdown 并发执行。
func (app *App) initializeLateProvider(registration *providerRegistration) error {
	if registration == nil {
		return nil
	}
	lifecycle := &app.providers
	lifecycle.phaseMu.Lock()
	defer lifecycle.phaseMu.Unlock()
	lifecycle.lock.Lock()
	if !lifecycle.initialized || lifecycle.registrationClosed || lifecycle.closing || lifecycle.closed || registration.initialized {
		lifecycle.lock.Unlock()
		return nil
	}
	registration.initializing = true
	lifecycle.lock.Unlock()

	var initializeErr error
	if _, ok := registration.provider.(ServiceProviderInitializer); ok {
		initializeErr = safeProviderInitialize(registration.provider, app)
	}
	lifecycle.lock.Lock()
	registration.initializing = false
	registration.initialized = true
	if initializeErr != nil {
		lifecycle.initializeErr = errors.Join(lifecycle.initializeErr, fmt.Errorf("服务提供者 %T 初始化失败: %w", registration.provider, initializeErr))
	}
	lifecycle.condition.Broadcast()
	lifecycle.lock.Unlock()
	return initializeErr
}

// BootProviders 关闭注册阶段，等待在途 Register，并按确定顺序启动全部 Provider。
// 该公开方法保留只启动已注册 Provider 的历史契约；App.Run 和 HTTP
// 使用 EnsureReady 执行完整项目初始化。已经失败的初始化不允许绕过。
func (app *App) BootProviders() error {
	if app == nil {
		return ErrNilApplication
	}
	app.lifecycle.transitionLock.Lock()
	defer app.lifecycle.transitionLock.Unlock()
	app.lifecycle.lock.Lock()
	state := app.lifecycle.state
	closed := app.lifecycle.closed
	app.lifecycle.lock.Unlock()
	if closed || state == ApplicationStateClosing || state == ApplicationStateClosed {
		return ErrApplicationClosed
	}
	if state == ApplicationStateFailed {
		return app.failedLifecycleError()
	}
	if state == ApplicationStateRunning {
		return nil
	}
	if state == ApplicationStateInitializing {
		if err := app.initializeLocked(); err != nil {
			return err
		}
	}
	return app.bootProviders()
}

// bootProviders 只执行 Provider 启动阶段；调用方必须持有应用转换锁并完成初始化判定。
func (app *App) bootProviders() error {
	if app == nil {
		return ErrNilApplication
	}
	lifecycle := &app.providers
	lifecycle.lock.Lock()
	if lifecycle.closed || lifecycle.closing {
		lifecycle.lock.Unlock()
		return ErrProviderLifecycleClosed
	}
	lifecycle.lock.Unlock()
	lifecycle.lock.Lock()
	initializeStarted := lifecycle.initializing || lifecycle.initialized
	lifecycle.lock.Unlock()
	if initializeStarted {
		if initializeErr := app.initializeProviders(); initializeErr != nil {
			app.recordStartupError(initializeErr)
			return initializeErr
		}
	}
	lifecycle.phaseMu.Lock()
	defer lifecycle.phaseMu.Unlock()
	lifecycle.lock.Lock()
	lifecycle.initializeLocked()
	if lifecycle.closed || lifecycle.closing {
		lifecycle.lock.Unlock()
		return ErrProviderLifecycleClosed
	}
	if lifecycle.booted {
		err := lifecycle.bootErr
		lifecycle.lock.Unlock()
		return err
	}
	lifecycle.registrationClosed = true
	lifecycle.booting = true
	for lifecycle.registering > 0 {
		lifecycle.condition.Wait()
	}
	initializeErr := error(nil)
	if initializeStarted {
		lifecycle.lock.Unlock()
		initializeErr = app.initializePendingProviders()
		lifecycle.lock.Lock()
	}
	providers := lifecycle.snapshotProvidersLocked()
	lifecycle.lock.Unlock()
	app.lifecycle.lock.Lock()
	if app.lifecycle.state != ApplicationStateRunning {
		app.lifecycle.state = ApplicationStateBooting
	}
	app.lifecycle.lock.Unlock()

	bootErr := initializeErr
	if bootErr == nil {
		bootErr = app.dispatchAppInit()
	}
	if bootErr == nil {
		for _, provider := range providers {
			if err := safeProviderBoot(provider, app); err != nil {
				bootErr = fmt.Errorf("服务提供者 %T 启动失败: %w", provider, err)
				break
			}
		}
	}
	lifecycle.lock.Lock()
	lifecycle.booting = false
	lifecycle.booted = true
	lifecycle.bootErr = bootErr
	lifecycle.condition.Broadcast()
	lifecycle.lock.Unlock()
	app.lifecycle.lock.Lock()
	if bootErr != nil {
		app.lifecycle.state = ApplicationStateFailed
	} else if app.lifecycle.state == ApplicationStateBooting {
		app.lifecycle.state = ApplicationStateInitialized
	}
	app.lifecycle.lock.Unlock()
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
	if app.event == nil {
		return nil
	}
	if err := app.event.Dispatch(currentEvent); err != nil {
		return fmt.Errorf("分发生命周期事件 %q 失败: %w", currentEvent.Name(), err)
	}
	return nil
}

// dispatchAppInit 保证 AppInit 在 Initialize 中触发一次；直接使用底层
// BootProviders 的扩展场景仍会在启动服务前补发同一事件。
func (app *App) dispatchAppInit() error {
	if app == nil {
		return ErrNilApplication
	}
	app.appInitOnce.Do(func() {
		app.appInitErr = app.dispatchLifecycleEvent(event.NewAppInitEvent())
	})
	return app.appInitErr
}

func (app *App) shutdownProviders() error {
	lifecycle := &app.providers
	lifecycle.phaseMu.Lock()
	defer lifecycle.phaseMu.Unlock()
	lifecycle.lock.Lock()
	lifecycle.initializeLocked()
	// 先阻止新的 Register，再等待已经进入 Register 的 Provider 完成；这些 Provider
	// 仍然需要经过 Initialize，不能直接进入 Shutdown。
	lifecycle.closing = true
	lifecycle.condition.Broadcast()
	for lifecycle.registering > 0 || lifecycle.booting {
		lifecycle.condition.Wait()
	}
	shouldInitializePending := lifecycle.initialized
	lifecycle.lock.Unlock()

	var initializeErr error
	if shouldInitializePending {
		initializeErr = app.initializePendingProviders()
	}

	lifecycle.lock.Lock()
	lifecycle.registrationClosed = true
	lifecycle.closed = true
	// 关闭阶段沿用所有成功 Register 的 Provider；此时所有待初始化 Provider
	// 已在上面的串行阶段完成 Initialize，避免释放未完成装配的资源。
	providers := lifecycle.snapshotProvidersLocked()
	lifecycle.lock.Unlock()

	shutdownErrors := make([]error, 0, 1)
	if initializeErr != nil {
		shutdownErrors = append(shutdownErrors, initializeErr)
	}
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

func safeProviderInitialize(provider ServiceProvider, app *App) (err error) {
	defer recoverProviderPanic("Initialize", &err)
	return provider.(ServiceProviderInitializer).Initialize(app)
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
