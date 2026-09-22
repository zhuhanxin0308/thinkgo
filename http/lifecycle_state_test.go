package http

import (
	stdcontext "context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/metrics"
)

// lifecycleStartWriter 在真实监听器完成绑定后通知测试协程。
type lifecycleStartWriter struct {
	once    sync.Once
	started chan struct{}
}

// lifecycleHTTPKernel 用可取消上下文驱动真实 HTTP 监听，
// 验证它可以安全嵌套在 App.Run 的根应用租约内。
type lifecycleHTTPKernel struct {
	handler *Http
	ctx     stdcontext.Context
}

func (kernel *lifecycleHTTPKernel) Run() error {
	return kernel.handler.ListenContext(kernel.ctx)
}

func (writer *lifecycleStartWriter) Write(content []byte) (int, error) {
	writer.once.Do(func() { close(writer.started) })
	return len(content), nil
}

// bootReplacingMetricsProvider 模拟 Provider 在 Boot 阶段替换 HTTP 依赖服务。
type bootReplacingMetricsProvider struct {
	replacement *metrics.Registry
}

func (*bootReplacingMetricsProvider) Register(*framework.App) error { return nil }

func (provider *bootReplacingMetricsProvider) Boot(app *framework.App) error {
	return app.Instance(string(framework.ServiceMetrics), provider.replacement)
}

// startLifecycleHTTPServer 启动真实 TCP 监听并等待绑定成功。
func startLifecycleHTTPServer(t *testing.T, handler *Http) (stdcontext.CancelFunc, <-chan error) {
	t.Helper()
	handler.srvConf.Host = "127.0.0.1"
	handler.srvConf.Port = 0
	handler.srvConf.ShutdownTimeout = 500 * time.Millisecond
	started := make(chan struct{})
	handler.startupOutput = &lifecycleStartWriter{started: started}
	ctx, cancel := stdcontext.WithCancel(stdcontext.Background())
	done := make(chan error, 1)
	go func() { done <- handler.ListenContext(ctx) }()
	select {
	case <-started:
	case err := <-done:
		cancel()
		t.Fatalf("HTTP 监听在绑定前退出: %v", err)
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("等待 HTTP 监听绑定超时")
	}
	return cancel, done
}

// TestHTTPInitializationWaitsForCompleteApplication 验证 Initialized 标志提前置位时，
// HTTP 仍会等待完整初始化结果，不能读取半成品服务或提前 Boot。
func TestHTTPInitializationWaitsForCompleteApplication(t *testing.T) {
	basePath := t.TempDir()
	ensureHTTPTestConfigFiles(t, basePath)
	app := framework.NewConsoleAppUninitialized(basePath)
	loaderStarted := make(chan struct{})
	allowLoader := make(chan struct{})
	if err := app.RegisterApplicationLoader(func(*framework.App) error {
		close(loaderStarted)
		<-allowLoader
		return nil
	}); err != nil {
		t.Fatalf("注册阻塞应用加载器失败: %v", err)
	}
	initializeDone := make(chan error, 1)
	go func() { initializeDone <- app.Initialize() }()
	select {
	case <-loaderStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("应用初始化未进入阻塞加载器")
	}

	handler, constructErr := NewHttp(app)
	if constructErr != nil {
		close(allowLoader)
		<-initializeDone
		t.Fatalf("HTTP 构造不得读取半初始化服务: %v", constructErr)
	}
	readyDone := make(chan error, 1)
	go func() { readyDone <- handler.ensureInitialized() }()
	var prematureErr error
	premature := false
	select {
	case prematureErr = <-readyDone:
		premature = true
	case <-time.After(30 * time.Millisecond):
	}
	close(allowLoader)
	initializeErr := <-initializeDone
	readyErr := prematureErr
	if !premature {
		readyErr = <-readyDone
	}
	if premature {
		t.Fatalf("HTTP 在应用初始化完成前提前返回: %v", prematureErr)
	}
	if initializeErr != nil || readyErr != nil {
		t.Fatalf("释放加载器后应用应完整就绪: initialize=%v ready=%v", initializeErr, readyErr)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("关闭半初始化并发测试应用失败: %v", err)
	}
}

// TestHTTPReloadsServicesAfterProviderBoot 验证 NewHttp 的构造期兼容校验
// 不会让 Boot 前的旧服务快照泄漏到请求运行期。
func TestHTTPReloadsServicesAfterProviderBoot(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	replacement := metrics.NewRegistry()
	if err := app.RegisterProvider(&bootReplacingMetricsProvider{replacement: replacement}); err != nil {
		t.Fatalf("注册 Boot 服务替换 Provider 失败: %v", err)
	}
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建服务快照测试 HTTP 内核失败: %v", err)
	}
	if err := handler.ensureInitialized(); err != nil {
		t.Fatalf("启动服务快照测试 HTTP 内核失败: %v", err)
	}
	if handler.metrics != replacement {
		t.Fatal("HTTP 必须在 Provider Boot 后重新解析最终服务快照")
	}
}

// TestHTTPListenReloadsServicesAfterRunLease 验证 ensureInitialized 之后、
// 监听租约之前的合法服务重绑定会进入最终运行快照。
func TestHTTPListenReloadsServicesAfterRunLease(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	handler := newTestHTTPHandler(t, app)
	if err := handler.ensureInitialized(); err != nil {
		t.Fatalf("初始化租约后快照测试 HTTP 内核失败: %v", err)
	}
	replacement := metrics.NewRegistry()
	if err := app.Instance(string(framework.ServiceMetrics), replacement); err != nil {
		t.Fatalf("监听前替换指标服务失败: %v", err)
	}
	if handler.metrics == replacement {
		t.Fatal("测试前置条件错误: ensureInitialized 快照不应自动跟随后续重绑定")
	}
	cancel, done := startLifecycleHTTPServer(t, handler)
	runtimeMetrics := handler.metrics
	runtimePort := handler.srvConf.Port
	cancel()
	listenErr := <-done

	if !errors.Is(listenErr, stdcontext.Canceled) {
		t.Fatalf("取消服务快照测试监听应返回 context.Canceled，实际为 %v", listenErr)
	}
	if runtimeMetrics != replacement {
		t.Fatal("监听必须在获取运行租约后重新解析最终指标服务")
	}
	if runtimePort != 0 {
		t.Fatalf("租约后服务刷新不得覆盖已准备的动态监听端口，实际为 %d", runtimePort)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("关闭租约后快照测试应用失败: %v", err)
	}
}

// TestAppRunSupportsRealHTTPKernel 验证 App.Run 完成项目初始化并进入
// Running 后，真实 HTTP 内核仍能完成二次就绪检查、嵌套租约和 TCP 监听。
func TestAppRunSupportsRealHTTPKernel(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配 App.Run HTTP 测试端口失败: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("释放 App.Run HTTP 测试端口失败: %v", err)
	}
	configuration := mustHTTPConfig(t, app)
	if err := configuration.Set("app.server.port", port); err != nil {
		t.Fatalf("设置 App.Run HTTP 测试端口失败: %v", err)
	}
	if err := configuration.Set("app.server.shutdown_timeout_ms", 500); err != nil {
		t.Fatalf("设置 App.Run HTTP 关闭超时失败: %v", err)
	}
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建 App.Run 真实 HTTP 内核失败: %v", err)
	}
	started := make(chan struct{})
	handler.startupOutput = &lifecycleStartWriter{started: started}
	ctx, cancel := stdcontext.WithCancel(stdcontext.Background())
	app.Kernel = &lifecycleHTTPKernel{handler: handler, ctx: ctx}
	runDone := make(chan error, 1)
	go func() { runDone <- app.Run() }()
	select {
	case <-started:
	case runErr := <-runDone:
		cancel()
		t.Fatalf("App.Run 中的 HTTP 内核在监听前退出: %v", runErr)
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("等待 App.Run 真实 HTTP 监听超时")
	}
	if app.State() != framework.ApplicationStateRunning {
		cancel()
		t.Fatalf("真实 HTTP 监听期间应用应为 Running，实际为 %s", app.State())
	}
	cancel()
	runErr := <-runDone
	if !errors.Is(runErr, stdcontext.Canceled) {
		t.Fatalf("取消 App.Run HTTP 内核应保留 context.Canceled，实际为 %v", runErr)
	}
	if app.State() != framework.ApplicationStateClosed {
		t.Fatalf("App.Run HTTP 返回后应关闭应用，实际为 %s", app.State())
	}
}

// TestNativeApplicationsAreRunningWhileListening 验证一个真实监听租约同时覆盖
// 根 App 和所有原生子应用，并在监听退出后统一释放。
func TestNativeApplicationsAreRunningWhileListening(t *testing.T) {
	basePath := t.TempDir()
	writeNativeHTTPConfig(t, basePath, `{
  "app_env":"test",
  "default_app":"index",
  "app_map":{},
  "domain_bind":{},
  "deny_app_list":[],
  "server":{"host":"127.0.0.1","port":8080},
  "compression":{"enable":false}
}`)
	app := framework.NewConsoleAppUninitialized(basePath)
	if err := app.RegisterApplications(
		nativeHTTPGlobalLoader,
		framework.ApplicationDefinition{Name: "index", Register: nativeHTTPApplicationLoader("index")},
		framework.ApplicationDefinition{Name: "admin", Register: nativeHTTPApplicationLoader("admin")},
	); err != nil {
		t.Fatalf("注册监听生命周期应用失败: %v", err)
	}
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建监听生命周期 HTTP 内核失败: %v", err)
	}
	if err := handler.ensureInitialized(); err != nil {
		t.Fatalf("初始化监听生命周期 HTTP 内核失败: %v", err)
	}
	cancel, done := startLifecycleHTTPServer(t, handler)
	rootState := app.State()
	childStates := make(map[string]framework.ApplicationState, len(handler.applicationHost.applications))
	for name, child := range handler.applicationHost.applications {
		childStates[name] = child.app.State()
	}
	cancel()
	listenErr := <-done

	if !errors.Is(listenErr, stdcontext.Canceled) {
		t.Fatalf("取消监听应返回 context.Canceled，实际为 %v", listenErr)
	}
	if rootState != framework.ApplicationStateRunning {
		t.Fatalf("监听期间根应用应为 Running，实际为 %s", rootState)
	}
	for name, state := range childStates {
		if state != framework.ApplicationStateRunning {
			t.Errorf("监听期间子应用 %q 应为 Running，实际为 %s", name, state)
		}
	}
	if app.State() != framework.ApplicationStateInitialized {
		t.Fatalf("监听租约释放后根应用应回到 Initialized，实际为 %s", app.State())
	}
	for name, child := range handler.applicationHost.applications {
		if child.app.State() != framework.ApplicationStateInitialized {
			t.Errorf("监听租约释放后子应用 %q 状态错误: %s", name, child.app.State())
		}
	}
	if err := app.Close(); err != nil {
		t.Fatalf("关闭监听生命周期应用失败: %v", err)
	}
}

// TestNativeApplicationsReloadServicesAfterRunLeases 验证根处理器、
// 复用根 App 的主应用和独立子应用都在全部租约获取后刷新最终快照。
func TestNativeApplicationsReloadServicesAfterRunLeases(t *testing.T) {
	basePath := t.TempDir()
	writeNativeHTTPConfig(t, basePath, `{
  "app_env":"test",
  "default_app":"index",
  "app_map":{},
  "domain_bind":{},
  "deny_app_list":[],
  "server":{"host":"127.0.0.1","port":8080},
  "compression":{"enable":false}
}`)
	app := framework.NewConsoleAppUninitialized(basePath)
	if err := app.RegisterApplications(
		nativeHTTPGlobalLoader,
		framework.ApplicationDefinition{Name: "index", Register: nativeHTTPApplicationLoader("index")},
		framework.ApplicationDefinition{Name: "admin", Register: nativeHTTPApplicationLoader("admin")},
	); err != nil {
		t.Fatalf("注册子应用快照测试应用失败: %v", err)
	}
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建子应用快照测试 HTTP 内核失败: %v", err)
	}
	if err := handler.ensureInitialized(); err != nil {
		t.Fatalf("初始化子应用快照测试 HTTP 内核失败: %v", err)
	}
	indexHandler := handler.applicationHost.applications["index"]
	adminHandler := handler.applicationHost.applications["admin"]
	if indexHandler == nil || adminHandler == nil {
		t.Fatal("子应用快照测试未构建 index/admin 处理器")
	}
	rootReplacement := metrics.NewRegistry()
	adminReplacement := metrics.NewRegistry()
	if err := app.Instance(string(framework.ServiceMetrics), rootReplacement); err != nil {
		t.Fatalf("监听前替换根应用指标服务失败: %v", err)
	}
	if err := adminHandler.app.Instance(string(framework.ServiceMetrics), adminReplacement); err != nil {
		t.Fatalf("监听前替换 admin 指标服务失败: %v", err)
	}
	cancel, done := startLifecycleHTTPServer(t, handler)
	rootMetrics := handler.metrics
	indexMetrics := indexHandler.metrics
	adminMetrics := adminHandler.metrics
	runtimePort := handler.srvConf.Port
	cancel()
	listenErr := <-done

	if !errors.Is(listenErr, stdcontext.Canceled) {
		t.Fatalf("取消子应用快照测试监听应返回 context.Canceled，实际为 %v", listenErr)
	}
	if rootMetrics != rootReplacement || indexMetrics != rootReplacement {
		t.Fatal("根处理器与主应用必须共同发布根 App 的最终服务快照")
	}
	if adminMetrics != adminReplacement {
		t.Fatal("admin 子应用未发布租约后的最终服务快照")
	}
	if runtimePort != 0 {
		t.Fatalf("子应用服务刷新不得覆盖根监听端口，实际为 %d", runtimePort)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("关闭子应用快照测试应用失败: %v", err)
	}
}

// TestHTTPRunLeaseRejectsMutationAndClose 验证真实监听期间服务快照被冻结，
// 外部变更和资源关闭均以 ErrApplicationRunning 失败。
func TestHTTPRunLeaseRejectsMutationAndClose(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	handler := newTestHTTPHandler(t, app)
	if err := handler.ensureInitialized(); err != nil {
		t.Fatalf("初始化运行租约测试 HTTP 内核失败: %v", err)
	}
	cancel, done := startLifecycleHTTPServer(t, handler)
	mutationErr := app.Instance("runtime.mutation", "blocked")
	closeErr := app.Close()
	cancel()
	listenErr := <-done

	if !errors.Is(listenErr, stdcontext.Canceled) {
		t.Fatalf("取消监听应返回 context.Canceled，实际为 %v", listenErr)
	}
	if !errors.Is(mutationErr, framework.ErrApplicationRunning) {
		t.Fatalf("监听期间服务变更应返回 ErrApplicationRunning，实际为 %v", mutationErr)
	}
	if !errors.Is(closeErr, framework.ErrApplicationRunning) {
		t.Fatalf("监听期间 Close 应返回 ErrApplicationRunning，实际为 %v", closeErr)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("监听租约释放后关闭应用失败: %v", err)
	}
}

// TestHTTPRejectsConcurrentListen 验证同一 HTTP 内核不会因可嵌套的
// 应用租约而意外允许两个独立监听生命周期。
func TestHTTPRejectsConcurrentListen(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	handler := newTestHTTPHandler(t, app)
	if err := handler.ensureInitialized(); err != nil {
		t.Fatalf("初始化并发监听测试 HTTP 内核失败: %v", err)
	}
	cancel, done := startLifecycleHTTPServer(t, handler)
	secondErr := handler.ListenContext(stdcontext.Background())
	cancel()
	listenErr := <-done

	if !errors.Is(secondErr, ErrHTTPServerRunning) {
		t.Fatalf("并发监听应返回 ErrHTTPServerRunning，实际为 %v", secondErr)
	}
	if !errors.Is(listenErr, stdcontext.Canceled) {
		t.Fatalf("取消首个监听应返回 context.Canceled，实际为 %v", listenErr)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("关闭并发监听测试应用失败: %v", err)
	}
}
