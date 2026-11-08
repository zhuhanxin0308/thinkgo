package serve

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
)

const (
	executeTestDeadline     = 10 * time.Second
	executeShutdownTimeout  = time.Second
	executeFixtureTenantID  = int64(1)
	executeFixtureUserID    = int64(1)
	executeFixtureProductID = int64(1)
)

// requireExecuteIntegration 只复用运行器显式准备的依赖，不创建或重置共享业务数据。
func requireExecuteIntegration(t *testing.T) business.Config {
	t.Helper()
	if os.Getenv("BENCH_INTEGRATION") != "1" {
		t.Skip("应用装配测试需要运行器显式准备真实PostgreSQL和Redis")
	}
	configuration, err := business.LoadConfig()
	if err != nil {
		t.Fatalf("读取运行器提供的业务配置失败: %v", err)
	}
	return configuration
}

// readableExecuteService 同时验证缓存访问和真实数据库查询，避免仅凭构造成功判断依赖可用。
func readableExecuteService(ctx context.Context, service *business.Service) error {
	principal := business.Principal{
		TenantID: executeFixtureTenantID,
		UserID:   executeFixtureUserID,
	}
	product := strconv.FormatInt(executeFixtureProductID, 10)
	detail, cache, err := service.Detail(ctx, principal, product)
	if err != nil {
		return fmt.Errorf("读取已初始化的商品失败: %w", err)
	}
	if detail.ID != executeFixtureProductID || (cache != "hit" && cache != "miss") {
		return fmt.Errorf("商品或缓存结果不符合种子合同: id=%d cache=%q", detail.ID, cache)
	}
	catalog, err := service.Catalog(ctx, principal, nil)
	if err != nil {
		return fmt.Errorf("读取真实数据库目录失败: %w", err)
	}
	if len(catalog.Items) == 0 {
		return errors.New("已初始化的商品目录为空")
	}
	return nil
}

// requireExecuteServiceClosed 分别验证Redis和数据库已关闭，避免只验证其中一种依赖。
func requireExecuteServiceClosed(t *testing.T, service *business.Service, configuration business.Config) {
	t.Helper()
	if service == nil {
		t.Fatal("应用工厂没有取得已打开的业务服务")
	}

	ctx, cancel := context.WithTimeout(context.Background(), executeTestDeadline)
	defer cancel()
	principal := business.Principal{
		TenantID: executeFixtureTenantID,
		UserID:   executeFixtureUserID,
	}
	product := strconv.FormatInt(executeFixtureProductID, 10)
	if _, _, err := service.Detail(ctx, principal, product); !errors.Is(err, redis.ErrClosed) {
		t.Fatalf("应用退出后Redis客户端仍可用或错误语义不正确: %v", err)
	}

	// 从已关闭的标准数据库句柄取得关闭错误，不读取业务服务的私有字段或匹配错误文案。
	reference, err := sql.Open("postgres", configuration.DatabaseURL)
	if err != nil {
		t.Fatalf("创建数据库关闭状态参照失败: %v", err)
	}
	if err := reference.Close(); err != nil {
		t.Fatalf("关闭数据库状态参照失败: %v", err)
	}
	closedError := reference.PingContext(context.Background())
	if closedError == nil {
		t.Fatal("已关闭的数据库状态参照仍接受操作")
	}
	if _, err := service.Catalog(ctx, principal, nil); !errors.Is(err, closedError) {
		t.Fatalf("应用退出后数据库仍可用或错误语义不正确: %v", err)
	}
}

func TestExecuteRejectsInvalidConfigurationBeforeFactory(t *testing.T) {
	t.Setenv("BENCH_ADDR", "invalid-listen-address")
	called := false
	err := execute(context.Background(), func(business.Config, *business.Service) (Application, error) {
		called = true
		return nil, errors.New("无效配置不应到达应用工厂")
	})
	if err == nil {
		t.Fatal("无效应用配置没有阻止启动")
	}
	if called {
		t.Fatal("配置校验失败后仍调用了应用工厂")
	}
}

func TestExecuteCanceledContextDoesNotCallFactory(t *testing.T) {
	requireExecuteIntegration(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := execute(ctx, func(business.Config, *business.Service) (Application, error) {
		called = true
		return nil, errors.New("取消的启动不应到达应用工厂")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("启动取消错误丢失: %v", err)
	}
	if called {
		t.Fatal("依赖打开被取消后仍调用了应用工厂")
	}
}

func TestExecuteFactoryFailureClosesRealDependencies(t *testing.T) {
	configuration := requireExecuteIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), executeTestDeadline)
	defer cancel()

	factoryError := errors.New("应用工厂构造失败")
	var captured *business.Service
	err := execute(ctx, func(_ business.Config, service *business.Service) (Application, error) {
		captured = service
		// 即使关闭断言失败，也释放本测试打开的连接；重复关闭结果不作为测试判据。
		t.Cleanup(func() { _ = service.Close() })
		if err := readableExecuteService(ctx, service); err != nil {
			return nil, err
		}
		return nil, factoryError
	})
	if !errors.Is(err, factoryError) {
		t.Fatalf("工厂错误未保留，或工厂调用前依赖不可用: %v", err)
	}
	requireExecuteServiceClosed(t, captured, configuration)
}

// serviceCheckingApplication 复用既有关闭探针，并验证运行和应用关闭阶段仍拥有可用依赖。
type serviceCheckingApplication struct {
	applicationProbe
	service           *business.Service
	runServiceError   error
	closeServiceError error
}

func (application *serviceCheckingApplication) Run(ctx context.Context) error {
	*application.order = append(*application.order, "run")
	application.runServiceError = readableExecuteService(ctx, application.service)
	return errors.Join(application.applicationProbe.Run(ctx), application.runServiceError)
}

func (application *serviceCheckingApplication) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), executeTestDeadline)
	defer cancel()
	application.closeServiceError = readableExecuteService(ctx, application.service)
	return errors.Join(application.applicationProbe.Close(), application.closeServiceError)
}

func TestExecuteClosesRealDependenciesAfterApplication(t *testing.T) {
	configuration := requireExecuteIntegration(t)
	runError := errors.New("应用运行失败")
	cases := []struct {
		name     string
		runError error
	}{
		{name: "正常运行"},
		{name: "运行失败", runError: runError},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), executeTestDeadline)
			defer cancel()

			var order []string
			var application *serviceCheckingApplication
			var captured *business.Service
			err := execute(ctx, func(_ business.Config, service *business.Service) (Application, error) {
				captured = service
				t.Cleanup(func() { _ = service.Close() })
				application = &serviceCheckingApplication{
					applicationProbe: applicationProbe{
						closingProbe: closingProbe{order: &order, name: "application"},
						err:          testCase.runError,
					},
					service: service,
				}
				return application, nil
			})
			if application == nil {
				t.Fatalf("真实依赖没有完成应用装配: %v", err)
			}
			if application.runServiceError != nil || application.closeServiceError != nil {
				t.Fatalf(
					"业务依赖在应用运行或关闭前失效: run=%v close=%v",
					application.runServiceError, application.closeServiceError,
				)
			}
			if testCase.runError == nil && err != nil {
				t.Fatalf("正常应用退出失败: %v", err)
			}
			if testCase.runError != nil && !errors.Is(err, testCase.runError) {
				t.Fatalf("应用运行错误未保留: %v", err)
			}
			if !slices.Equal(order, []string{"run", "application"}) {
				t.Fatalf("应用运行和关闭次数或顺序错误: %v", order)
			}
			requireExecuteServiceClosed(t, captured, configuration)
		})
	}
}

func TestRunOwnedOnlyNormalizesCancellationForCanceledContext(t *testing.T) {
	runError := errors.New("独立运行错误")
	applicationCloseError := errors.New("应用关闭错误")
	dependencyCloseError := errors.New("依赖关闭错误")
	cases := []struct {
		name                  string
		cancelContext         bool
		runError              error
		applicationCloseError error
		dependencyCloseError  error
		wantErrors            []error
	}{
		{
			name:       "活动上下文保留取消错误",
			runError:   context.Canceled,
			wantErrors: []error{context.Canceled},
		},
		{
			name:          "已取消上下文正常结束",
			cancelContext: true,
			runError:      context.Canceled,
		},
		{
			name:          "已取消上下文保留其他运行错误",
			cancelContext: true,
			runError:      runError,
			wantErrors:    []error{runError},
		},
		{
			name:                  "正常取消仍保留全部关闭错误",
			cancelContext:         true,
			runError:              context.Canceled,
			applicationCloseError: applicationCloseError,
			dependencyCloseError:  dependencyCloseError,
			wantErrors:            []error{applicationCloseError, dependencyCloseError},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if testCase.cancelContext {
				cancel()
			}

			var order []string
			application := applicationProbe{
				closingProbe: closingProbe{
					order: &order,
					name:  "application",
					err:   testCase.applicationCloseError,
				},
				err: testCase.runError,
			}
			dependencies := closingProbe{
				order: &order,
				name:  "dependencies",
				err:   testCase.dependencyCloseError,
			}
			err := RunOwned(ctx, application, dependencies)
			if (err == nil) != (len(testCase.wantErrors) == 0) {
				t.Fatalf("运行或关闭错误的成功状态不正确: %v", err)
			}
			for _, wanted := range testCase.wantErrors {
				if !errors.Is(err, wanted) {
					t.Fatalf("运行或关闭错误丢失: want=%v actual=%v", wanted, err)
				}
			}
			wantCancellation := !testCase.cancelContext && errors.Is(testCase.runError, context.Canceled)
			if errors.Is(err, context.Canceled) != wantCancellation {
				t.Fatalf("上下文取消的归一化条件错误: %v", err)
			}
			if !slices.Equal(order, []string{"application", "dependencies"}) {
				t.Fatalf("取消或错误导致资源关闭顺序改变: %v", order)
			}
		})
	}
}

func TestServeListenerReturnsClosedListenerError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), executeTestDeadline)
	defer cancel()
	err = serveListener(ctx, listener, http.NotFoundHandler(), executeShutdownTimeout)
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("已关闭监听器的启动错误被隐藏: %v", err)
	}
}

// awaitExecuteSignal 使用有限等待诊断失败；通道建立先后关系，不依赖调度耗时。
func awaitExecuteSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	timer := time.NewTimer(executeTestDeadline)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatalf("%s未在测试期限内完成", description)
	}
}

func TestServeListenerForceClosesInflightRequestAfterShutdownDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	t.Cleanup(cancelRequest)

	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: executeTestDeadline}
	request, err := http.NewRequestWithContext(
		requestContext, http.MethodGet, "http://"+listener.Addr().String(), nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	disconnected := make(chan struct{})
	handlerStopped := make(chan struct{})
	handlerRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(handlerRelease) }) }

	handler := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		defer close(handlerStopped)
		close(entered)
		select {
		case <-request.Context().Done():
			close(disconnected)
		case <-handlerRelease:
			return
		}
		// 即使连接关闭，也由测试显式释放处理器，保证正常排空在截止前无法完成。
		<-handlerRelease
	})

	serverResult := make(chan error, 1)
	serverStopped := make(chan struct{})
	go func() {
		defer close(serverStopped)
		serverResult <- serveListener(ctx, listener, handler, executeShutdownTimeout)
	}()

	clientResult := make(chan error, 1)
	clientStopped := make(chan struct{})
	go func() {
		defer close(clientStopped)
		response, requestError := client.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		clientResult <- requestError
	}()

	t.Cleanup(func() {
		// 失败清理先解除所有等待，再确认测试拥有的协程退出。
		cancel()
		releaseHandler()
		cancelRequest()
		_ = listener.Close()
		transport.CloseIdleConnections()
		for _, pending := range []struct {
			done <-chan struct{}
			name string
		}{
			{done: serverStopped, name: "服务协程"},
			{done: clientStopped, name: "客户端协程"},
		} {
			timer := time.NewTimer(executeTestDeadline)
			select {
			case <-pending.done:
			case <-timer.C:
				t.Errorf("%s在清理后仍未退出", pending.name)
			}
			timer.Stop()
		}
		select {
		case <-entered:
			timer := time.NewTimer(executeTestDeadline)
			defer timer.Stop()
			select {
			case <-handlerStopped:
			case <-timer.C:
				t.Error("请求处理器在清理后仍未退出")
			}
		default:
		}
	})

	awaitExecuteSignal(t, entered, "在途请求进入处理器")
	cancel()
	awaitExecuteSignal(t, serverStopped, "服务停止")
	if err := <-serverResult; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("未完成请求的排空超时被隐藏: %v", err)
	}

	// 客户端尚未取消；这里必须由服务端强制关闭连接使请求上下文结束。
	awaitExecuteSignal(t, disconnected, "服务端强制关闭请求连接")
	awaitExecuteSignal(t, clientStopped, "客户端观察连接关闭")
	clientError := <-clientResult
	if clientError == nil ||
		errors.Is(clientError, context.Canceled) ||
		errors.Is(clientError, context.DeadlineExceeded) {
		t.Fatalf("客户端没有观察到服务端关闭，或只是自身超时/取消: %v", clientError)
	}

	releaseHandler()
	awaitExecuteSignal(t, handlerStopped, "在途请求处理器退出")
}
