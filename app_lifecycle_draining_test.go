package framework

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/health"
)

const drainTestWaitLimit = 2 * time.Second

func runDrainTestOperation(operation func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- operation() }()
	return done
}

func awaitDrainTestOperation(t *testing.T, stage string, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(drainTestWaitLimit):
		t.Fatalf("%s 超时", stage)
		return nil
	}
}

// TestApplicationDrainingPreservesActiveDependencies 验证等待和重试窗口内的准入、探针及服务边界。
func TestApplicationDrainingPreservesActiveDependencies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		child := &App{container: NewContainer(), lifecycle: appLifecycle{state: ApplicationStateInitialized, initialized: true}}
		root := &App{applicationCatalog: &applicationCatalog{
			built: true, names: []string{"child"}, applications: map[string]*App{"child": child},
		}}
		const key = "worker"
		worker := new(int)
		if err := child.initializeProviders(); err != nil {
			t.Fatal(err)
		}
		if err := child.Instance(key, worker); err != nil {
			t.Fatal(err)
		}
		probes := health.NewRegistry()
		child.registerStartupReadinessCheckTo(probes)
		if status := probes.Readiness(t.Context()).HTTPStatus(); status != http.StatusOK {
			t.Fatalf("停机前应就绪，得到 %d", status)
		}
		task, err := child.AcquireRequestTask(1, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer task.Release()
		result := make(chan error, 1)
		go func() { result <- root.Close() }()
		synctest.Wait()
		checkDraining := func(app *App) {
			t.Helper()
			if state := app.State().String(); state != "draining" {
				t.Errorf("请求入口封闭后的状态为 %s，期望 draining", state)
			}
			if task, err := app.AcquireRequestTask(1, time.Second); !errors.Is(err, ErrApplicationClosed) {
				task.Release()
				t.Errorf("排空时接纳了业务请求: %v", err)
			}
			if task, err := app.AcquireOperationalRequestTask(time.Second); !errors.Is(err, ErrApplicationClosed) {
				task.Release()
				t.Errorf("排空时接纳了运维请求: %v", err)
			}
		}
		checkDraining(root)
		checkDraining(child)
		if status := probes.Readiness(t.Context()).HTTPStatus(); status != http.StatusServiceUnavailable {
			t.Errorf("排空中的就绪探针应返回 503，得到 %d", status)
		}
		if status := probes.Liveness().HTTPStatus(); status != http.StatusOK {
			t.Errorf("排空中的进程仍然存活，得到 %d", status)
		}
		if err := <-result; !errors.Is(err, ErrRequestTasksPending) {
			t.Fatalf("排空超时未保留错误类型: %v", err)
		}
		for _, app := range []*App{root, child} {
			checkDraining(app)
			if err := app.EnsureReady(); !errors.Is(err, ErrApplicationClosed) {
				t.Errorf("排空超时后仍可就绪: %v", err)
			}
			if err := app.BootProviders(); !errors.Is(err, ErrApplicationClosed) {
				t.Errorf("排空超时后仍可启动 Provider: %v", err)
			}
			if lease, err := app.AcquireRunLease(); !errors.Is(err, ErrApplicationClosed) {
				lease.Release()
				t.Errorf("排空超时后仍可取得运行租约: %v", err)
			}
		}
		if err := child.Initialize(); !errors.Is(err, ErrApplicationClosed) {
			t.Errorf("排空后仍能通过初始化入口: %v", err)
		}
		var order []string
		provider := &initializationStageProvider{name: "late", order: &order}
		if err := child.RegisterProvider(provider); !errors.Is(err, ErrProviderRegistrationClosed) || len(order) != 0 {
			t.Errorf("排空后仍执行迟到的 Provider 注册或初始化: 调用=%v 错误=%v", order, err)
		}
		service := &stagedApplicationService{}
		if err := child.Register(service); !errors.Is(err, ErrProviderRegistrationClosed) || service.registered.Load() != 0 || service.initialized.Load() != 0 {
			t.Errorf("排空后仍执行应用服务注册或初始化: 错误=%v", err)
		}
		if err := child.Instance(key, new(int)); !errors.Is(err, ErrApplicationClosed) {
			t.Errorf("排空时仍可替换在途任务依赖: %v", err)
		}
		if got, err := child.ResolveService(key); err != nil || got != worker {
			t.Fatalf("在途任务丢失已有依赖: 实例=%v 错误=%v", got, err)
		}
		scope, err := child.NewScope()
		if err != nil {
			t.Fatal(err)
		}
		if got, err := scope.Make(key); err != nil || got != worker {
			t.Errorf("在途任务无法从作用域解析依赖: 实例=%v 错误=%v", got, err)
		}
		if err := scope.Close(); err != nil {
			t.Fatal(err)
		}
		task.Release()
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		if root.State() != ApplicationStateClosed || child.State() != ApplicationStateClosed {
			t.Errorf("重试后应用未关闭: 根=%s 子=%s", root.State(), child.State())
		}
		if _, err := child.ResolveService(key); !errors.Is(err, ErrApplicationClosed) {
			t.Errorf("释放资源后仍能解析依赖: %v", err)
		}
	})
}

// TestApplicationDrainingWaitsForOwnedRunLease 验证子应用的既有运行租约保留根应用的重试机会。
func TestApplicationDrainingWaitsForOwnedRunLease(t *testing.T) {
	child := &App{}
	root := &App{applicationCatalog: &applicationCatalog{
		built: true, names: []string{"child"}, applications: map[string]*App{"child": child},
	}}
	lease, err := child.AcquireRunLease()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err := root.Close(); !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("子应用的运行租约必须阻止关闭: %v", err)
	}
	if root.State().String() != "draining" || child.State().String() != "draining" {
		t.Errorf("持有租约时应保留资源等待重试: 根=%s 子=%s", root.State(), child.State())
	}
	if nested, err := child.AcquireRunLease(); !errors.Is(err, ErrApplicationClosed) {
		nested.Release()
		t.Errorf("子应用排空后仍能续租: %v", err)
	}
	lease.Release()
	if err := root.Close(); err != nil || root.State() != ApplicationStateClosed || child.State() != ApplicationStateClosed {
		t.Fatalf("释放运行租约后未能关闭全部应用: 根=%s 子=%s 错误=%v", root.State(), child.State(), err)
	}
}

// TestApplicationDrainingRejectsStartupBeforeTasksFinish 验证在途任务检查启动入口不会耗尽自己的关闭预算。
func TestApplicationDrainingRejectsStartupBeforeTasksFinish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		app := &App{}
		task, err := app.AcquireRequestTask(1, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer task.Release()
		closeDone := make(chan error, 1)
		go func() { closeDone <- app.Close() }()
		synctest.Wait()
		// 已通过锁前检查的启动者也必须能立即拿锁复查，不能由手里的任务阻塞自己。
		if !app.lifecycle.transitionLock.TryLock() {
			t.Fatal("关闭正在等待任务时仍持有转换锁")
		}
		err = app.ensureReadyLocked()
		app.lifecycle.transitionLock.Unlock()
		if !errors.Is(err, ErrApplicationClosed) {
			t.Fatalf("已排队的启动者未在锁内识别关闭: %v", err)
		}
		for _, start := range []func() error{app.Initialize, app.EnsureReady, app.BootProviders, app.Run} {
			if err := start(); !errors.Is(err, ErrApplicationClosed) {
				t.Errorf("准入封闭后启动入口未返回关闭错误: %v", err)
			}
		}
		if lease, err := app.AcquireRunLease(); !errors.Is(err, ErrApplicationClosed) {
			lease.Release()
			t.Errorf("准入封闭后获得新运行租约: %v", err)
		}
		if err := app.ApplyProjectApplicationOverrides(map[string]interface{}{"app_name": "drain"}); !errors.Is(err, ErrApplicationClosed) {
			t.Errorf("准入封闭后仍可覆盖启动配置: %v", err)
		}
		task.Release()
		if err := <-closeDone; err != nil {
			t.Fatalf("启动入口等待自身请求排空导致关闭失败: %v", err)
		}
	})
}

// TestClosedApplicationRetainsShutdownResult 验证重复关闭立即返回首次释放结果，不再等待子应用转换。
func TestClosedApplicationRetainsShutdownResult(t *testing.T) {
	child := &App{}
	root := &App{applicationCatalog: &applicationCatalog{
		built: true, names: []string{"child"}, applications: map[string]*App{"child": child},
	}}
	shutdownErr := errors.New("资源关闭失败")
	var order []string
	if err := root.RegisterProvider(&shutdownTrackingProvider{name: "root", order: &order, err: shutdownErr}); err != nil {
		t.Fatal(err)
	}
	firstErr := awaitDrainTestOperation(t, "首次关闭", runDrainTestOperation(root.Close))
	if !errors.Is(firstErr, shutdownErr) {
		t.Fatalf("关闭未保留资源错误: %v", firstErr)
	}
	child.lifecycle.transitionLock.Lock()
	unlock := sync.OnceFunc(child.lifecycle.transitionLock.Unlock)
	defer unlock()
	retry := runDrainTestOperation(root.Close)
	select {
	case err := <-retry:
		if err != firstErr || len(order) != 1 {
			t.Errorf("关闭结果受后续子转换影响: 首次=%v 再次=%v 释放顺序=%v", firstErr, err, order)
		}
	case <-time.After(drainTestWaitLimit):
		unlock()
		_ = awaitDrainTestOperation(t, "解除子转换锁后的重复关闭", retry)
		t.Fatal("重复关闭等待了子应用转换锁")
	}
}

// TestApplicationDrainingProtectsOwnedStartup 验证子应用的在途启动回调仍可使用根依赖。
func TestApplicationDrainingProtectsOwnedStartup(t *testing.T) {
	for _, phase := range []string{"initialize", "boot", "lease"} {
		t.Run(phase, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			child := &App{container: NewContainer()}
			root := &App{container: NewContainer()}
			const key = "shared"
			shared := new(int)
			if err := root.Instance(key, shared); err != nil {
				t.Fatal(err)
			}
			var callbackErr error
			callback := func() {
				close(started)
				<-release
				if err := child.Instance("assembly", shared); err != nil {
					callbackErr = fmt.Errorf("已有启动回调不能完成装配: %w", err)
					return
				}
				if phase == "initialize" {
					if err := child.RegisterRouteLoader(func(*App) error { return nil }); err != nil {
						callbackErr = fmt.Errorf("既有初始化回调不能登记路由: %w", err)
						return
					}
					if err := child.RegisterProvider(&callbackProvider{}); err != nil {
						callbackErr = fmt.Errorf("既有初始化回调不能登记 Provider: %w", err)
						return
					}
				}
				if got, err := root.ResolveService(key); err != nil || got != shared {
					callbackErr = fmt.Errorf("子应用启动回调失去根应用依赖: 实例=%v 错误=%v", got, err)
				}
			}
			start := child.BootProviders
			if phase == "initialize" {
				base := t.TempDir()
				writeTestAppConfigFiles(t, base)
				child = NewConsoleAppUninitialized(base)
				if err := child.RegisterApplicationLoader(func(*App) error { callback(); return nil }); err != nil {
					t.Fatal(err)
				}
				start = child.Initialize
			} else {
				if err := child.RegisterProvider(&callbackProvider{boot: callback}); err != nil {
					t.Fatal(err)
				}
				if phase == "lease" {
					start = func() error {
						lease, err := child.AcquireRunLease()
						lease.Release()
						return err
					}
				}
			}
			root.applicationCatalog = &applicationCatalog{
				built: true, names: []string{"child"}, applications: map[string]*App{"child": child},
			}
			startDone := runDrainTestOperation(start)
			select {
			case <-started:
			case err := <-startDone:
				t.Fatalf("未进入启动回调: %v", err)
			case <-time.After(drainTestWaitLimit):
				unblock()
				_ = awaitDrainTestOperation(t, "解除回调门禁后的子启动", startDone)
				t.Fatal("子应用未按时进入启动回调")
			}
			closeDone := runDrainTestOperation(root.Close)
			select {
			case err := <-closeDone:
				if !errors.Is(err, ErrApplicationRunning) {
					t.Errorf("子应用正在启动时应允许重试: %v", err)
				}
			case <-time.After(drainTestWaitLimit):
				unblock()
				_ = awaitDrainTestOperation(t, "解除回调门禁后的子启动", startDone)
				_ = awaitDrainTestOperation(t, "子启动结束后的首次关闭", closeDone)
				t.Fatal("根应用在子应用启动回调中阻塞")
			}
			if root.State() != ApplicationStateDraining || child.State() != ApplicationStateDraining {
				t.Errorf("启动回调完成前已开始释放资源: 根=%s 子=%s", root.State(), child.State())
			}
			unblock()
			err := awaitDrainTestOperation(t, "子应用启动收尾", startDone)
			if callbackErr != nil {
				t.Error(callbackErr)
			}
			if phase == "lease" {
				if !errors.Is(err, ErrApplicationClosed) {
					t.Errorf("排空后在途启动仍可取得新租约: %v", err)
				}
			} else if err != nil {
				t.Errorf("在途启动阶段未能完成: %v", err)
			}
			if child.State() != ApplicationStateDraining {
				t.Errorf("启动尾声覆盖了已封闭的状态: %s", child.State())
			}
			if err := awaitDrainTestOperation(t, "根应用重试关闭", runDrainTestOperation(root.Close)); err != nil || child.State() != ApplicationStateClosed {
				t.Fatalf("子应用启动回调完成后重试失败: 子=%s 错误=%v", child.State(), err)
			}
		})
	}
}
