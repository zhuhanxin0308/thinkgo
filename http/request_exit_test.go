package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/cleanup"
)

type exitingExceptionHandler struct {
	applicationExceptionHandlerProbe
}

func (handler *exitingExceptionHandler) Report(interface{}) {
	handler.reports.Add(1)
	runtime.Goexit()
}

type requestExitCloser struct{ close func() error }

func (closer *requestExitCloser) Close() error { return closer.close() }

func waitForRequestGoexit(t *testing.T, run func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Errorf("请求产生意外 panic：%v", recovered)
			}
		}()
		run()
		t.Error("发生 Goexit 的请求不应返回调用方")
	}()
	select {
	case <-done:
	case <-time.After(protocolTestTimeout):
		t.Fatal("扩展异常退出后请求未在预算内结束")
	}
}

// 请求工厂还未交付 Request、异常处理器还未交付 Response 时，原持有者负责清理。
func TestRequestGoexitDuringProviderOrRecovery(t *testing.T) {
	for _, entry := range []string{"Run", "ServeHTTP"} {
		for _, phase := range []string{"Provider", "Recovery"} {
			t.Run(entry+"/"+phase, func(t *testing.T) {
				app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
				var closes atomic.Int32
				if err := app.BindScoped("exit_probe", func() interface{} { return &requestScopedHTTPService{closed: &closes} }); err != nil {
					t.Fatal(err)
				}
				if err := app.BindFactory(string(framework.ServiceRequest), func(resolver *framework.Container, raw *http.Request, options ...fwcontext.RequestOption) (*fwcontext.Request, error) {
					if _, err := resolver.Make("exit_probe"); err != nil {
						t.Error(err)
					}
					if phase == "Provider" {
						runtime.Goexit()
					}
					return fwcontext.NewRequest(raw, options...)
				}); err != nil {
					t.Fatal(err)
				}
				exceptionHandler := &exitingExceptionHandler{}
				if phase == "Recovery" {
					if err := app.Instance(string(framework.ServiceExceptionHandle), exceptionHandler); err != nil {
						t.Fatal(err)
					}
					if err := app.Event().Listen(event.EventHttpRun, &event.SimpleListener{Handler: func(event.Event) error {
						return errors.New("测试请求异常处理")
					}}); err != nil {
						t.Fatal(err)
					}
				}
				handler := newTestHTTPHandler(t, app)
				waitForRequestGoexit(t, func() {
					if entry == "Run" {
						handler.Run()
					} else {
						handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
					}
				})
				if snapshot := app.RequestTaskSnapshot(); snapshot.Active != 0 || closes.Load() != 1 {
					t.Fatalf("扩展退出后遗留租约或作用域：%+v, closes=%d", snapshot, closes.Load())
				}
				if phase == "Recovery" && exceptionHandler.reports.Load() != 1 {
					t.Fatalf("异常处理器实际执行了 %d 次", exceptionHandler.reports.Load())
				}
			})
		}
	}
}

// 即使执行中途退出且无响应可以传给 End，租约也必须覆盖实际收尾并且只归还一次。
func TestRequestGoexitKeepsTaskUntilCleanup(t *testing.T) {
	for _, entry := range []string{"Run", "ServeHTTP"} {
		t.Run(entry, func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
			var closes atomic.Int32
			var interrupted atomic.Bool
			closeStarted := make(chan struct{})
			allowClose := make(chan struct{})
			finishClose := sync.OnceFunc(func() { close(allowClose) })
			defer finishClose()
			if err := app.BindScoped("exit_probe", func() interface{} {
				return &requestExitCloser{close: func() error {
					close(closeStarted)
					<-allowClose
					closes.Add(1)
					return nil
				}}
			}); err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			resume := sync.OnceFunc(func() { close(release) })
			defer resume()
			mustHTTPMiddleware(t, app).PipeLifecycle(func(request *fwcontext.Request, _ func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
				if interrupted.Swap(true) {
					return fwcontext.NewResponse().Content("ready")
				}
				if _, err := request.Make("exit_probe"); err != nil {
					t.Error(err)
				}
				runtime.Goexit()
				return nil
			}, func(*fwcontext.Request, *fwcontext.Response) {
				select {
				case <-entered:
					return
				default:
					close(entered)
				}
				<-release
			})
			handler := newTestHTTPHandler(t, app)
			if err := handler.ensureInitialized(); err != nil {
				t.Fatal(err)
			}
			handler.srvConf.RequestEndTimeout = 20 * time.Millisecond
			waitForRequestGoexit(t, func() {
				if entry == "Run" {
					handler.Run()
				} else {
					handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
				}
			})
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("请求退出后未执行终结回调")
			}
			if snapshot := app.RequestTaskSnapshot(); snapshot.Active != 1 || snapshot.Completed != 0 || closes.Load() != 0 {
				t.Fatalf("阻塞的终结回调提前释放了请求资源：%+v, closes=%d", snapshot, closes.Load())
			}
			resume()
			select {
			case <-closeStarted:
			case <-time.After(protocolTestTimeout):
				t.Fatal("终结回调完成后没有开始关闭作用域")
			}
			if snapshot := app.RequestTaskSnapshot(); snapshot.Active != 1 || snapshot.Completed != 0 {
				t.Fatalf("关闭器仍在执行时租约已归还：%+v", snapshot)
			}
			finishClose()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := app.WaitRequestTasks(ctx); err != nil {
				t.Fatal(err)
			}
			if snapshot := app.RequestTaskSnapshot(); snapshot.Active != 0 || snapshot.Completed != 1 || closes.Load() != 1 {
				t.Fatalf("收尾后租约或作用域计数错误：%+v, closes=%d", snapshot, closes.Load())
			}
			if len(handler.requestTasks) != 0 || len(handler.endStates) != 0 {
				t.Fatal("已退出请求仍有不可达的收尾记录")
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
			if recorder.Code != http.StatusOK || recorder.Body.String() != "ready" || app.RequestTaskSnapshot().Completed != 2 {
				t.Fatal("中断收尾后未能正常接纳并完成下一请求")
			}
		})
	}
}

// 结束事件或终结回调退出监督协程时，后面的终结回调和作用域仍须按顺序执行。
func TestRequestEndGoexitCompletesRemainingStages(t *testing.T) {
	for _, stage := range []string{"HttpEnd", "Terminate"} {
		t.Run(stage, func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
			var order []string
			firstEventErr := errors.New("首个结束监听器失败")
			lastEventErr := errors.New("末个结束监听器失败")
			closeErr := errors.New("作用域关闭失败")
			if err := app.BindScoped("exit_probe", func() interface{} {
				return &requestExitCloser{close: func() error {
					order = append(order, "close")
					return closeErr
				}}
			}); err != nil {
				t.Fatal(err)
			}
			dispatcher := app.Event()
			if err := dispatcher.Listen(event.EventHttpEnd, &event.SimpleListener{Handler: func(event.Event) error {
				order = append(order, "first event")
				return firstEventErr
			}}); err != nil {
				t.Fatal(err)
			}
			if stage == "HttpEnd" {
				if err := dispatcher.Listen(event.EventHttpEnd, &event.SimpleListener{Handler: func(event.Event) error {
					order = append(order, "exit event")
					runtime.Goexit()
					return nil
				}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := dispatcher.Listen(event.EventHttpEnd, &event.SimpleListener{Handler: func(event.Event) error {
				order = append(order, "last event")
				return lastEventErr
			}}); err != nil {
				t.Fatal(err)
			}
			pipeline := mustHTTPMiddleware(t, app)
			pipeline.PipeLifecycle(func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
				if _, err := request.Make("exit_probe"); err != nil {
					t.Error(err)
				}
				return next(request)
			}, func(*fwcontext.Request, *fwcontext.Response) {
				order = append(order, "first terminate")
				if stage == "Terminate" {
					runtime.Goexit()
				}
			})
			pipeline.PipeLifecycle(func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
				return next(request)
			}, func(*fwcontext.Request, *fwcontext.Response) {
				order = append(order, "last terminate")
			})
			handler := newTestHTTPHandler(t, app)
			response := handler.Run()
			err := handler.endWithContext(t.Context(), response)
			for _, want := range []error{firstEventErr, cleanup.ErrAborted, lastEventErr, closeErr} {
				if !errors.Is(err, want) {
					t.Errorf("收尾结果遗漏 %v：%v", want, err)
				} else if strings.Count(err.Error(), want.Error()) != 1 {
					t.Errorf("收尾结果重复记录 %v：%v", want, err)
				}
			}
			wantOrder := []string{"first event"}
			if stage == "HttpEnd" {
				wantOrder = append(wantOrder, "exit event")
			}
			wantOrder = append(wantOrder, "last event", "first terminate", "last terminate", "close")
			if snapshot := app.RequestTaskSnapshot(); snapshot.Active != 0 || snapshot.Completed != 1 || !slices.Equal(order, wantOrder) {
				t.Fatalf("后续收尾遗漏、重复或乱序：%+v, got=%v, want=%v", snapshot, order, wantOrder)
			}
		})
	}
}

// 父宿主没有取得响应时，实际执行请求的子应用仍须独自完成收尾。
func TestDelegatedRunGoexitClosesSelectedScopeOnce(t *testing.T) {
	kernel := newBenchmarkMultiHTTPHandler(t)
	selected := kernel.applicationHost.applications["admin"]
	selected.middleware.Pipe(func(*fwcontext.Request, func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		runtime.Goexit()
		return nil
	})
	callerScope := &directRunScopeProbe{}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/admin/health", nil), fwcontext.WithServiceScope(callerScope, callerScope))
	waitForRequestGoexit(t, func() { kernel.Run(request) })
	if snapshot := selected.app.RequestTaskSnapshot(); snapshot.Active != 0 || snapshot.Completed != 1 || callerScope.closed != 1 {
		t.Fatalf("子应用租约或调用方作用域未正确释放：%+v, closes=%d", snapshot, callerScope.closed)
	}
	if snapshot := kernel.app.RequestTaskSnapshot(); snapshot.Completed != 0 || len(kernel.applicationHost.endHandlers) != 0 || len(kernel.endStates) != 0 || len(selected.endStates) != 0 {
		t.Fatalf("父宿主持有重复的收尾状态：%+v", snapshot)
	}
}

// 响应流尚未提交就 panic，宿主再次渲染异常时退出，也必须回收已有响应身份。
func TestServeHTTPGoexitDuringStreamRecovery(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	exceptionHandler := &exitingExceptionHandler{}
	if err := app.Instance(string(framework.ServiceExceptionHandle), exceptionHandler); err != nil {
		t.Fatal(err)
	}
	var closes atomic.Int32
	if err := app.BindScoped("stream_probe", func() interface{} { return &requestScopedHTTPService{closed: &closes} }); err != nil {
		t.Fatal(err)
	}
	if _, err := mustHTTPRoute(t, app).Get("/stream", func(request *fwcontext.Request) *fwcontext.Response {
		if _, err := request.Make("stream_probe"); err != nil {
			t.Error(err)
		}
		return fwcontext.NewResponse().Stream(func(io.Writer) error { panic("流在发送前失败") })
	}); err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, app)
	waitForRequestGoexit(t, func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))
	})
	if snapshot := app.RequestTaskSnapshot(); exceptionHandler.reports.Load() != 1 || snapshot.Active != 0 || snapshot.Completed != 1 || closes.Load() != 1 || len(handler.endStates) != 0 {
		t.Fatalf("宿主异常渲染退出后遗留收尾：%+v, reports=%d, closes=%d, states=%d",
			snapshot, exceptionHandler.reports.Load(), closes.Load(), len(handler.endStates))
	}
}
