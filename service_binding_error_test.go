package framework

import (
	"errors"
	"testing"
)

type serviceBindingErrorController struct{}

// TestFoundationBindingPropagatesLifecycleRejection 验证基础服务装配不会吞掉容器冻结错误。
func TestFoundationBindingPropagatesLifecycleRejection(t *testing.T) {
	app, release, runDone := runningServiceBindingTestApp(t)

	err := app.bindFoundationServices()
	if !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("运行态基础服务装配应返回 ErrApplicationRunning，实际为 %v", err)
	}

	close(release)
	if err = <-runDone; err != nil {
		t.Fatalf("结束测试应用失败: %v", err)
	}
}

// TestControllerBindingPropagatesLifecycleRejection 验证控制器工厂装配不会吞掉容器冻结错误。
func TestControllerBindingPropagatesLifecycleRejection(t *testing.T) {
	app := &App{container: NewContainer(), lifecycle: appLifecycle{state: ApplicationStateConstructed}}
	if err := app.RegisterController("Lifecycle", &serviceBindingErrorController{}); err != nil {
		t.Fatalf("注册测试控制器失败: %v", err)
	}
	kernel := &blockingAppRunKernel{started: make(chan struct{}), release: make(chan struct{})}
	app.Kernel = kernel
	runDone := make(chan error, 1)
	go func() { runDone <- app.Run() }()
	<-kernel.started

	err := app.initializeControllerBindings()
	if !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("运行态控制器装配应返回 ErrApplicationRunning，实际为 %v", err)
	}

	close(kernel.release)
	if err = <-runDone; err != nil {
		t.Fatalf("结束测试应用失败: %v", err)
	}
}

// TestRuntimeProvidersPropagateLifecycleRejection 验证运行时 Provider 无论是否进入退化装配，都保留容器冻结根因。
func TestRuntimeProvidersPropagateLifecycleRejection(t *testing.T) {
	tests := []struct {
		name       string
		initialize func(*App) error
	}{
		{name: "cache", initialize: (&appCacheProvider{}).Initialize},
		{name: "filesystem", initialize: (&appFilesystemProvider{}).Initialize},
		{name: "cookie", initialize: (&appCookieProvider{}).Initialize},
		{name: "database", initialize: (&appDatabaseProvider{}).Initialize},
		{name: "lang", initialize: (&appLangProvider{}).Initialize},
		{name: "log", initialize: (&appLogProvider{}).Initialize},
		{name: "session", initialize: (&appSessionProvider{}).Initialize},
		{name: "view", initialize: (&appViewProvider{}).Initialize},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := NewAppUninitialized(t.TempDir())
			app.lifecycle.lock.Lock()
			app.lifecycle.state = ApplicationStateRunning
			app.lifecycle.lock.Unlock()

			err := test.initialize(app)
			if !errors.Is(err, ErrApplicationRunning) {
				t.Fatalf("运行态 %s Provider 应返回 ErrApplicationRunning，实际为 %v", test.name, err)
			}
			if closeErr := app.Close(); closeErr != nil {
				t.Fatalf("关闭 %s Provider 测试应用失败: %v", test.name, closeErr)
			}
		})
	}
}

func runningServiceBindingTestApp(t *testing.T) (*App, chan struct{}, chan error) {
	t.Helper()
	kernel := &blockingAppRunKernel{started: make(chan struct{}), release: make(chan struct{})}
	app := &App{container: NewContainer(), Kernel: kernel, lifecycle: appLifecycle{state: ApplicationStateConstructed}}
	runDone := make(chan error, 1)
	go func() { runDone <- app.Run() }()
	<-kernel.started
	return app, kernel.release, runDone
}
