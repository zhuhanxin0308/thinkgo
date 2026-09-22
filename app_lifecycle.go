package framework

import (
	"errors"
	"fmt"
	"sync"

	"github.com/zhuhanxin0308/thinkgo/v3/log"
)

const maxStartupErrors = 64

// ApplicationState 表示应用实例当前所处的生命周期阶段。
type ApplicationState uint8

const (
	// ApplicationStateInvalid 表示不存在或尚未构造完成的应用状态。
	ApplicationStateInvalid ApplicationState = iota
	// ApplicationStateConstructed 表示应用已构造但尚未初始化运行时资源。
	ApplicationStateConstructed
	// ApplicationStateInitializing 表示应用正在初始化配置和运行时资源。
	ApplicationStateInitializing
	// ApplicationStateInitialized 表示应用初始化完成但尚未运行内核。
	ApplicationStateInitialized
	// ApplicationStateBooting 表示应用正在启动 Provider。
	ApplicationStateBooting
	// ApplicationStateRunning 表示应用内核正在运行。
	ApplicationStateRunning
	// ApplicationStateClosing 表示应用正在释放资源。
	ApplicationStateClosing
	// ApplicationStateClosed 表示应用已经关闭。
	ApplicationStateClosed
	// ApplicationStateFailed 表示应用初始化或启动失败。
	ApplicationStateFailed
)

// String 返回生命周期状态的稳定文本，便于日志和诊断输出。
func (state ApplicationState) String() string {
	switch state {
	case ApplicationStateInvalid:
		return "invalid"
	case ApplicationStateConstructed:
		return "constructed"
	case ApplicationStateInitializing:
		return "initializing"
	case ApplicationStateInitialized:
		return "initialized"
	case ApplicationStateBooting:
		return "booting"
	case ApplicationStateRunning:
		return "running"
	case ApplicationStateClosing:
		return "closing"
	case ApplicationStateClosed:
		return "closed"
	case ApplicationStateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

var (
	// ErrNilApplication 表示方法接收到了 nil 应用实例。
	ErrNilApplication = errors.New("应用实例不能为空")
	// ErrApplicationClosed 表示应用资源已经关闭。
	ErrApplicationClosed = errors.New("应用已经关闭")
	// ErrApplicationRunning 表示同一应用正在运行，禁止重复启动。
	ErrApplicationRunning = errors.New("应用正在运行")
	// ErrApplicationFailed 表示应用已经进入不可绕过的启动失败状态。
	ErrApplicationFailed = errors.New("应用启动已经失败")
	// ErrKernelUnavailable 表示应用未装配运行内核。
	ErrKernelUnavailable = errors.New("应用内核未初始化")
	// ErrApplicationInitializationPanic 表示初始化 panic 已转换为启动错误。
	ErrApplicationInitializationPanic = errors.New("应用初始化发生 panic")
	// ErrKernelPanic 表示内核 Run panic 已转换为错误。
	ErrKernelPanic = errors.New("应用内核运行发生 panic")
	// ErrResourceClosePanic 表示资源 Close panic 已转换为错误。
	ErrResourceClosePanic = errors.New("应用资源关闭发生 panic")
)

type appLifecycle struct {
	tasks                  requestTaskRegistry
	transitionLock         sync.Mutex
	initializationLock     sync.Mutex
	initializeOnce         sync.Once
	lock                   sync.Mutex
	requiresInitialization bool
	initialized            bool
	state                  ApplicationState
	running                bool
	runLeases              uint32
	closed                 bool
	closeOnce              sync.Once
	closeErr               error
}

// RunLease 表示应用运行资源的一个持有者。
// 多层宿主可以各自持有租约，只有最后一个租约释放后应用才退出 Running。
type RunLease struct {
	app  *App
	once sync.Once
}

// Initialize 只执行一次初始化，并在重复调用时返回首次初始化结果。
func (app *App) Initialize() error {
	if app == nil {
		return ErrNilApplication
	}
	app.lifecycle.transitionLock.Lock()
	defer app.lifecycle.transitionLock.Unlock()
	return app.initializeLocked()
}

// initializeLocked 执行初始化主体；调用方必须持有应用转换锁。
func (app *App) initializeLocked() error {
	app.lifecycle.initializationLock.Lock()
	defer app.lifecycle.initializationLock.Unlock()
	app.lifecycle.lock.Lock()
	if app.lifecycle.closed {
		app.lifecycle.lock.Unlock()
		return ErrApplicationClosed
	}
	if app.lifecycle.running {
		app.lifecycle.lock.Unlock()
		return ErrApplicationRunning
	}
	if app.lifecycle.initialized {
		app.lifecycle.lock.Unlock()
		return app.StartupError()
	}
	// ThinkPHP 在 initialize 开始时立即设置 initialized，随后才加载配置和 app 定义。
	// 运行状态仍由 state 区分 initializing 与 initialized，避免并发调用误用半成品资源。
	app.lifecycle.initialized = true
	app.lifecycle.state = ApplicationStateInitializing
	app.lifecycle.lock.Unlock()
	app.lifecycle.initializeOnce.Do(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				app.recordStartupError(fmt.Errorf("%w: %v", ErrApplicationInitializationPanic, recovered))
			}
			app.lifecycle.lock.Lock()
			if app.StartupError() != nil {
				app.lifecycle.state = ApplicationStateFailed
			} else {
				app.lifecycle.state = ApplicationStateInitialized
			}
			app.lifecycle.lock.Unlock()
		}()
		app.initialize()
	})
	return app.StartupError()
}

// Initialized 返回应用是否已经进入初始化阶段，对应 ThinkPHP App.initialized。
func (app *App) Initialized() bool {
	if app == nil {
		return false
	}
	app.lifecycle.lock.Lock()
	defer app.lifecycle.lock.Unlock()
	return app.lifecycle.initialized
}

// State 返回应用当前生命周期状态。
func (app *App) State() ApplicationState {
	if app == nil {
		return ApplicationStateInvalid
	}
	app.lifecycle.lock.Lock()
	defer app.lifecycle.lock.Unlock()
	if app.lifecycle.state == ApplicationStateInvalid {
		return ApplicationStateConstructed
	}
	return app.lifecycle.state
}

// EnsureReady 等待完整初始化并启动 Provider。
// 初始化或启动一旦失败，后续调用只返回同一启动错误，不允许绕过 Failed 状态。
func (app *App) EnsureReady() error {
	if app == nil {
		return ErrNilApplication
	}
	app.lifecycle.transitionLock.Lock()
	defer app.lifecycle.transitionLock.Unlock()
	return app.ensureReadyLocked()
}

func (app *App) ensureReadyLocked() error {
	app.lifecycle.lock.Lock()
	state := app.lifecycle.state
	requiresInitialization := app.lifecycle.requiresInitialization
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
	// 通过框架构造器创建的应用必须完成完整初始化；手工装配的历史零值应用
	// 仍可用于自定义内核和 Provider，以保持既有公开结构体用法兼容。
	if requiresInitialization {
		if err := app.initializeLocked(); err != nil {
			return err
		}
	}
	if startupErr := app.StartupError(); startupErr != nil {
		app.lifecycle.lock.Lock()
		app.lifecycle.state = ApplicationStateFailed
		app.lifecycle.lock.Unlock()
		return startupErr
	}
	return app.bootProviders()
}

func (app *App) failedLifecycleError() error {
	startupErr := app.StartupError()
	if startupErr == nil {
		return ErrApplicationFailed
	}
	return errors.Join(ErrApplicationFailed, startupErr)
}

// AcquireRunLease 在应用完全就绪后冻结运行期服务快照。
// 返回的租约必须 Release；Release 幂等且允许 HTTP 宿主嵌套持有。
func (app *App) AcquireRunLease() (*RunLease, error) {
	return app.acquireRunLease(false)
}

func (app *App) acquireRunLease(exclusive bool) (*RunLease, error) {
	if app == nil {
		return nil, ErrNilApplication
	}
	app.lifecycle.transitionLock.Lock()
	defer app.lifecycle.transitionLock.Unlock()
	app.lifecycle.lock.Lock()
	if app.lifecycle.closed {
		app.lifecycle.lock.Unlock()
		return nil, ErrApplicationClosed
	}
	if exclusive && app.lifecycle.runLeases > 0 {
		app.lifecycle.lock.Unlock()
		return nil, ErrApplicationRunning
	}
	app.lifecycle.lock.Unlock()
	if err := app.ensureReadyLocked(); err != nil {
		return nil, err
	}

	app.serviceMutationMu.Lock()
	defer app.serviceMutationMu.Unlock()
	app.lifecycle.lock.Lock()
	defer app.lifecycle.lock.Unlock()
	if app.lifecycle.closed || app.lifecycle.state == ApplicationStateClosing || app.lifecycle.state == ApplicationStateClosed {
		return nil, ErrApplicationClosed
	}
	if app.lifecycle.state == ApplicationStateFailed {
		return nil, app.failedLifecycleError()
	}
	if exclusive && app.lifecycle.runLeases > 0 {
		return nil, ErrApplicationRunning
	}
	app.lifecycle.runLeases++
	app.lifecycle.running = true
	app.lifecycle.state = ApplicationStateRunning
	return &RunLease{app: app}, nil
}

// Release 释放一个运行租约；最后一个租约释放后恢复为已初始化状态。
func (lease *RunLease) Release() {
	if lease == nil || lease.app == nil {
		return
	}
	lease.once.Do(func() {
		app := lease.app
		app.lifecycle.transitionLock.Lock()
		defer app.lifecycle.transitionLock.Unlock()
		app.serviceMutationMu.Lock()
		defer app.serviceMutationMu.Unlock()
		app.lifecycle.lock.Lock()
		defer app.lifecycle.lock.Unlock()
		if app.lifecycle.runLeases > 0 {
			app.lifecycle.runLeases--
		}
		if app.lifecycle.runLeases == 0 {
			app.lifecycle.running = false
			if !app.lifecycle.closed && app.lifecycle.state == ApplicationStateRunning {
				app.lifecycle.state = ApplicationStateInitialized
			}
		}
	})
}

// Run 启动 Provider 与内核，并在退出时关闭全部应用资源。
func (app *App) Run() (runErr error) {
	if app == nil {
		return ErrNilApplication
	}
	lease, err := app.acquireRunLease(true)
	if err != nil {
		if errors.Is(err, ErrApplicationRunning) || errors.Is(err, ErrApplicationClosed) {
			return err
		}
		app.logLifecycleError("应用启动失败", err)
		return errors.Join(fmt.Errorf("应用启动失败: %w", err), app.Close())
	}
	defer func() {
		lease.Release()
		runErr = errors.Join(runErr, app.Close())
	}()

	if app.Kernel == nil {
		app.logLifecycleError("应用内核未初始化", ErrKernelUnavailable)
		return ErrKernelUnavailable
	}
	if err := safeKernelRun(app.Kernel); err != nil {
		app.logLifecycleError("应用内核运行失败", err)
		return fmt.Errorf("应用内核运行失败: %w", err)
	}
	return nil
}

// Close 幂等释放会话回收器、Provider、缓存、数据库和日志资源；运行中由 Run 负责关闭。
func (app *App) Close() error {
	return app.closeInternal()
}

func (app *App) closeInternal() error {
	if app == nil {
		return ErrNilApplication
	}
	app.lifecycle.transitionLock.Lock()
	defer app.lifecycle.transitionLock.Unlock()
	app.lifecycle.initializationLock.Lock()
	defer app.lifecycle.initializationLock.Unlock()
	app.lifecycle.lock.Lock()
	if app.lifecycle.running {
		app.lifecycle.lock.Unlock()
		return ErrApplicationRunning
	}
	app.lifecycle.lock.Unlock()
	if err := app.drainRequestTasksForClose(); err != nil {
		return err
	}
	app.lifecycle.closeOnce.Do(func() {
		app.serviceMutationMu.Lock()
		app.lifecycle.lock.Lock()
		app.lifecycle.closed = true
		app.lifecycle.state = ApplicationStateClosing
		app.lifecycle.lock.Unlock()
		app.serviceMutationMu.Unlock()
		closeErrors := make([]error, 0, 1)
		if err := app.closeOwnedApplications(); err != nil {
			closeErrors = append(closeErrors, err)
		}
		if err := app.shutdownProviders(); err != nil {
			closeErrors = append(closeErrors, err)
		}
		if err := app.closeServiceResources(); err != nil {
			closeErrors = append(closeErrors, err)
		}
		app.lifecycle.closeErr = errors.Join(closeErrors...)
		app.lifecycle.lock.Lock()
		app.lifecycle.state = ApplicationStateClosed
		app.lifecycle.lock.Unlock()
	})
	return app.lifecycle.closeErr
}

func (app *App) recordStartupError(err error) {
	if app == nil || err == nil {
		return
	}
	app.startupMu.Lock()
	if app.startupErrorCount >= maxStartupErrors {
		app.startupErrorOmitted++
		app.startupMu.Unlock()
		return
	}
	app.startupErr = errors.Join(app.startupErr, err)
	app.startupErrorCount++
	app.startupMu.Unlock()
}

func (app *App) logLifecycleError(message string, err error) {
	if app.log != nil && err != nil {
		app.log.Error(message + ": " + log.SanitizeErrorText(err.Error()))
	}
}

func safeStopSessionGarbageCollector(stop func()) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("停止会话回收器发生 panic: %v", recovered)
		}
	}()
	stop()
	return nil
}

func safeKernelRun(kernel Kernel) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", ErrKernelPanic, recovered)
		}
	}()
	return kernel.Run()
}

func safeResourceClose(resourceName string, closeResource func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %s: %v", ErrResourceClosePanic, resourceName, recovered)
		}
	}()
	return closeResource()
}
