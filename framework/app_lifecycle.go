package framework

import (
	"errors"
	"fmt"
	"sync"
)

const maxStartupErrors = 64

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
	lock               sync.Mutex
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
	app.lifecycle.lock.Unlock()
	app.initializeOnce.Do(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				app.recordStartupError(fmt.Errorf("%w: %v", ErrApplicationInitializationPanic, recovered))
			}
		}()
		app.initialize()
	})
	return app.StartupError()
}

// Run 启动 Provider 与内核，并在退出时关闭全部应用资源。
func (app *App) Run() (runErr error) {
	if app == nil {
		return ErrNilApplication
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
	app.lifecycle.closed = true
	app.lifecycle.lock.Unlock()
	app.lifecycle.closeOnce.Do(func() {
		closeErrors := make([]error, 0, 5)
		if app.sessionGCStop != nil {
			if err := safeStopSessionGarbageCollector(app.sessionGCStop); err != nil {
				closeErrors = append(closeErrors, err)
			}
			app.sessionGCStop = nil
		}
		if err := app.shutdownProviders(); err != nil {
			closeErrors = append(closeErrors, err)
		}
		if app.Cache != nil {
			if err := safeResourceClose("应用缓存", app.Cache.Close); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("关闭应用缓存失败: %w", err))
			}
		}
		if app.DBManager != nil {
			if err := safeResourceClose("数据库管理器", app.DBManager.Close); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("关闭数据库管理器失败: %w", err))
			}
		} else if app.DB != nil {
			if err := safeResourceClose("数据库连接", app.DB.Close); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("关闭数据库连接失败: %w", err))
			}
		}
		if app.Log != nil {
			if err := safeResourceClose("应用日志", app.Log.Close); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("关闭应用日志失败: %w", err))
			}
		}
		app.lifecycle.closeErr = errors.Join(closeErrors...)
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
	if app.Log != nil && err != nil {
		app.Log.Error(message + ": " + err.Error())
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
