package framework

import (
	"errors"
	"fmt"
	"sync"
	"thinkgo/framework/log"
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
	initializationLock sync.Mutex
	initializeOnce     sync.Once
	lock               sync.Mutex
	initialized        bool
	state              ApplicationState
	running            bool
	closed             bool
	closeOnce          sync.Once
	closeErr           error
}

// Initialize 只执行一次初始化，并在重复调用时返回首次初始化结果。
func (app *App) Initialize() error {
	if app == nil {
		return ErrNilApplication
	}
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
	app.lifecycle.state = ApplicationStateInitializing
	app.lifecycle.lock.Unlock()
	app.lifecycle.initializeOnce.Do(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				app.recordStartupError(fmt.Errorf("%w: %v", ErrApplicationInitializationPanic, recovered))
			}
			app.lifecycle.lock.Lock()
			app.lifecycle.initialized = true
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

// Initialized 返回应用是否已经完成初始化阶段。
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

// Run 启动 Provider 与内核，并在退出时关闭全部应用资源。
func (app *App) Run() (runErr error) {
	if app == nil {
		return ErrNilApplication
	}
	if err := app.managerLifecycleError(); err != nil {
		return err
	}
	app.lifecycle.initializationLock.Lock()
	app.lifecycle.lock.Lock()
	if app.lifecycle.closed {
		app.lifecycle.lock.Unlock()
		app.lifecycle.initializationLock.Unlock()
		return ErrApplicationClosed
	}
	if app.lifecycle.running {
		app.lifecycle.lock.Unlock()
		app.lifecycle.initializationLock.Unlock()
		return ErrApplicationRunning
	}
	app.lifecycle.running = true
	app.lifecycle.lock.Unlock()
	app.lifecycle.initializationLock.Unlock()

	defer func() {
		closeErr := app.close(true)
		app.lifecycle.lock.Lock()
		app.lifecycle.running = false
		app.lifecycle.lock.Unlock()
		runErr = errors.Join(runErr, closeErr)
	}()

	if startupErr := app.StartupError(); startupErr != nil {
		app.logLifecycleError("应用启动失败", startupErr)
		return fmt.Errorf("应用启动失败: %w", startupErr)
	}
	if err := app.BootProviders(); err != nil {
		app.logLifecycleError("服务提供者启动失败", err)
		return err
	}
	if app.Kernel == nil {
		app.logLifecycleError("应用内核未初始化", ErrKernelUnavailable)
		return ErrKernelUnavailable
	}
	app.serviceMutationMu.Lock()
	app.lifecycle.lock.Lock()
	if !app.lifecycle.closed {
		app.lifecycle.state = ApplicationStateRunning
	}
	app.lifecycle.lock.Unlock()
	app.serviceMutationMu.Unlock()
	if err := safeKernelRun(app.Kernel); err != nil {
		app.logLifecycleError("应用内核运行失败", err)
		return fmt.Errorf("应用内核运行失败: %w", err)
	}
	return nil
}

// Close 幂等释放会话回收器、Provider、缓存、数据库和日志资源；运行中由 Run 负责关闭。
func (app *App) Close() error {
	return app.close(false)
}

func (app *App) close(fromRun bool) error {
	if !fromRun {
		if err := app.managerLifecycleError(); err != nil {
			return err
		}
	}
	return app.closeInternal(fromRun)
}

// closeFromApplicationManager 由应用管理器统一释放托管应用资源，绕过面向调用方的所有权保护。
func (app *App) closeFromApplicationManager() error {
	closeErr := app.closeInternal(true)
	app.clearRunningByApplicationManager()
	return closeErr
}

func (app *App) closeInternal(fromRun bool) error {
	if app == nil {
		return ErrNilApplication
	}
	app.lifecycle.initializationLock.Lock()
	defer app.lifecycle.initializationLock.Unlock()
	app.lifecycle.lock.Lock()
	if app.lifecycle.running && !fromRun {
		app.lifecycle.lock.Unlock()
		return ErrApplicationRunning
	}
	app.lifecycle.lock.Unlock()
	app.lifecycle.closeOnce.Do(func() {
		app.serviceMutationMu.Lock()
		app.lifecycle.lock.Lock()
		app.lifecycle.closed = true
		app.lifecycle.state = ApplicationStateClosing
		app.lifecycle.lock.Unlock()
		app.serviceMutationMu.Unlock()
		closeErrors := make([]error, 0, 1)
		if err := app.shutdownProviders(); err != nil {
			closeErrors = append(closeErrors, err)
		}
		app.lifecycle.closeErr = errors.Join(closeErrors...)
		app.lifecycle.lock.Lock()
		app.lifecycle.state = ApplicationStateClosed
		app.lifecycle.lock.Unlock()
	})
	return app.lifecycle.closeErr
}

// managerLifecycleError 防止外部绕过 ApplicationManager 直接改变托管应用生命周期。
func (app *App) managerLifecycleError() error {
	if app == nil || app.applicationManager == nil {
		return nil
	}
	manager := app.applicationManager
	manager.lock.Lock()
	defer manager.lock.Unlock()
	owned := false
	for _, candidate := range manager.applications {
		if candidate == app {
			owned = true
			break
		}
	}
	if !owned {
		return nil
	}
	if manager.closed {
		return ErrApplicationManagerClosed
	}
	if manager.booting || manager.runPending || manager.running || manager.closing {
		return ErrApplicationManagerRunning
	}
	return ErrApplicationManagerOwnsLifecycle
}

// markRunningByApplicationManager 同步托管应用的公开生命周期状态与管理器运行状态。
func (app *App) markRunningByApplicationManager() {
	if app == nil {
		return
	}
	app.serviceMutationMu.Lock()
	defer app.serviceMutationMu.Unlock()
	app.lifecycle.lock.Lock()
	if !app.lifecycle.closed {
		app.lifecycle.running = true
		app.lifecycle.state = ApplicationStateRunning
	}
	app.lifecycle.lock.Unlock()
}

// clearRunningByApplicationManager 清理管理器运行标记，确保关闭后的状态不可再次启动。
func (app *App) clearRunningByApplicationManager() {
	if app == nil {
		return
	}
	app.lifecycle.lock.Lock()
	app.lifecycle.running = false
	app.lifecycle.lock.Unlock()
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
