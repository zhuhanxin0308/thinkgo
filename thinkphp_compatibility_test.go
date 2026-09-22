package framework

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	fwroute "github.com/zhuhanxin0308/thinkgo/v3/route"
	frameworkVersion "github.com/zhuhanxin0308/thinkgo/v3/version"
)

// TestThinkPHPTopLevelRequestAndResponseTypes 验证控制器可使用与 think.Request、
// think.Response 对应的框架顶层类型，不需要了解 context 子包。
func TestThinkPHPTopLevelRequestAndResponseTypes(t *testing.T) {
	var request *Request = (*fwcontext.Request)(nil)
	var response *Response = (*fwcontext.Response)(nil)
	if request != nil || response != nil {
		t.Fatal("类型别名的零值应保持为空")
	}
}

// TestInitializeTriggersAppInitBeforeBoot 验证 AppInit 属于 App.Initialize，
// 且发生在应用服务 Boot 之前，与 ThinkPHP 的初始化器顺序一致。
func TestInitializeTriggersAppInitBeforeBoot(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app := NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	service := &thinkPHPStyleService{}
	if err := app.Register(service); err != nil {
		t.Fatalf("注册应用服务失败: %v", err)
	}
	appInitCalled := false
	if err := app.Event().Listen(event.EventAppInit, &event.SimpleListener{Handler: func(event.Event) error {
		appInitCalled = true
		if service.booted {
			t.Fatal("AppInit 触发时服务不应已经 Boot")
		}
		return nil
	}}); err != nil {
		t.Fatalf("监听 AppInit 失败: %v", err)
	}
	if err := app.Initialize(); err != nil {
		t.Fatalf("初始化应用失败: %v", err)
	}
	if !appInitCalled {
		t.Fatal("Initialize 应触发 AppInit")
	}
	if service.booted {
		t.Fatal("Initialize 不应提前执行服务 Boot")
	}
	if err := app.Boot(); err != nil {
		t.Fatalf("Boot 应启动应用服务: %v", err)
	}
	if !service.booted {
		t.Fatal("Boot 未执行应用服务")
	}
}

// TestThinkPHPRouteFacadeSupportsFluentRulesAndGroups 验证命名、参数规则、
// 中间件和分组均可按 ThinkPHP 的链式业务写法声明。
func TestThinkPHPRouteFacadeSupportsFluentRulesAndGroups(t *testing.T) {
	app := NewApp(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })

	called := false
	guard := middleware.Handler(func(request *Request, next func(*Request) *Response) *Response {
		called = true
		return next(request)
	})
	rule := app.Route().Get("users/:id", "user/read").
		Name("user.read").
		Pattern(map[string]string{"id": `\d+`}).
		Middleware(guard)
	if rule.Path() != "/users/:id" || rule.Method() != "GET" {
		t.Fatalf("链式路由信息错误: path=%q method=%q", rule.Path(), rule.Method())
	}

	app.Route().Group("api", func(group *RuleGroup) {
		group.Post("users", "user/save")
	})
	routes, err := app.Route().Routes()
	if err != nil {
		t.Fatalf("读取路由失败: %v", err)
	}
	if len(routes) != 2 || routes[1].Path != "/api/users" {
		t.Fatalf("分组路由错误: %#v", routes)
	}
	if called {
		t.Fatal("注册路由时不应执行中间件")
	}
}

// TestThinkPHPRouteFacadePanicsOnInvalidDefinition 验证无效业务路由在启动期
// 立即抛出，而不是要求每一条路由都手工传播 error。
func TestThinkPHPRouteFacadePanicsOnInvalidDefinition(t *testing.T) {
	app := NewApp(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })

	defer func() {
		value := recover()
		if value == nil {
			t.Fatal("无效路由应触发 panic")
		}
		err, ok := value.(error)
		if !ok || !errors.Is(err, fwroute.ErrInvalidRouteHandler) {
			t.Fatalf("panic 错误不正确: %#v", value)
		}
	}()
	app.Route().Get("broken", "MissingAction")
}

// TestAppRouteFacadeUsesThinkPHPCallStyle 验证业务路由可直接调用 Get 并取得规则对象，
// 不需要为每条静态路由编写重复的错误分支。
func TestAppRouteFacadeUsesThinkPHPCallStyle(t *testing.T) {
	app := NewApp(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	rule := app.Route().Get("think", "index/index")
	if rule == nil || rule.Path() != "/think" {
		t.Fatalf("ThinkPHP 路由门面注册结果错误: %#v", rule)
	}
}

// TestThinkPHPRouteFacadeSupportsView 验证 Route.View 使用 GET 路由渲染模板，
// 并把第三个参数作为模板变量传入。
func TestThinkPHPRouteFacadeSupportsView(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	viewPath := filepath.Join(basePath, "app", "view")
	if err := os.MkdirAll(viewPath, 0o755); err != nil {
		t.Fatalf("创建视图目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(viewPath, "welcome.html"), []byte("hello {{.name}}"), 0o600); err != nil {
		t.Fatalf("写入视图模板失败: %v", err)
	}
	app := NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Initialize(); err != nil {
		t.Fatalf("初始化应用失败: %v", err)
	}

	app.Route().View("welcome", "welcome", map[string]interface{}{"name": "ThinkPHP"})
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/welcome", nil))
	matched, _, err := app.Route().Match(request)
	if err != nil || matched == nil {
		t.Fatalf("视图路由匹配失败: route=%#v err=%v", matched, err)
	}
	handler, ok := matched.Handler().(func(*Request) *Response)
	if !ok {
		t.Fatalf("视图路由处理器类型错误: %T", matched.Handler())
	}
	response := handler(request)
	if response.GetStatus() != http.StatusOK || string(response.GetBody()) != "hello ThinkPHP" {
		t.Fatalf("视图路由响应错误: status=%d body=%q", response.GetStatus(), string(response.GetBody()))
	}
}

// TestThinkPHPRouteFacadeSupportsMethodSpecificMiss 验证 Miss 优先使用当前
// 请求方法的规则，再回退到星号规则。
func TestThinkPHPRouteFacadeSupportsMethodSpecificMiss(t *testing.T) {
	app := NewApp(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	app.Route().Miss("fallback/post", http.MethodPost)
	app.Route().Miss("fallback/all")

	for _, testCase := range []struct {
		method  string
		handler string
	}{
		{method: http.MethodPost, handler: "fallback/post"},
		{method: http.MethodGet, handler: "fallback/all"},
	} {
		request := fwcontext.MustNewRequest(httptest.NewRequest(testCase.method, "http://example.com/missing", nil))
		matched, _, err := app.Route().Match(request)
		if err != nil || matched == nil || matched.Handler() != testCase.handler {
			t.Fatalf("%s Miss 匹配错误: route=%#v err=%v", testCase.method, matched, err)
		}
	}
}

// TestNewAppMatchesThinkPHPSingleApplicationPaths 验证默认 App 采用根目录下的 app，
// 并保持由 Http 首次运行触发初始化的生命周期。
func TestNewAppMatchesThinkPHPSingleApplicationPaths(t *testing.T) {
	rootPath := t.TempDir()
	app := NewApp(rootPath)
	t.Cleanup(func() { _ = app.Close() })

	if app.Initialized() {
		t.Fatal("NewApp 必须与 ThinkPHP 一致，仅构造应用而不立即初始化")
	}
	if app.State() != ApplicationStateConstructed {
		t.Fatalf("新应用状态错误: %s", app.State())
	}

	wantPaths := map[string]string{
		"root":    filepath.Clean(rootPath),
		"base":    filepath.Join(rootPath, "app"),
		"app":     filepath.Join(rootPath, "app"),
		"runtime": filepath.Join(rootPath, "runtime"),
		"config":  filepath.Join(rootPath, "config"),
		"route":   filepath.Join(rootPath, "route"),
		"view":    filepath.Join(rootPath, "view"),
	}
	gotPaths := map[string]string{
		"root":    app.GetRootPath(),
		"base":    app.GetBasePath(),
		"app":     app.GetAppPath(),
		"runtime": app.GetRuntimePath(),
		"config":  app.GetConfigPath(),
		"route":   app.GetRoutePath(),
		"view":    app.ApplicationViewPath(),
	}
	for name, want := range wantPaths {
		if got := filepath.Clean(gotPaths[name]); got != filepath.Clean(want) {
			t.Errorf("%s 路径错误: want=%q got=%q", name, want, got)
		}
	}
}

// TestAppProvidesThinkPHPStyleCoreServiceAccess 验证业务代码可直接从 App 获取
// 常用核心服务，而不必理解服务名、泛型解析器或容器内部绑定。
func TestAppProvidesThinkPHPStyleCoreServiceAccess(t *testing.T) {
	app := NewApp(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })

	if app.Container() == nil {
		t.Fatal("App.Container 不应为空")
	}
	if app.Config() == nil {
		t.Fatal("App.Config 不应为空")
	}
	if app.Env() == nil {
		t.Fatal("App.Env 不应为空")
	}
	if app.Event() == nil {
		t.Fatal("App.Event 不应为空")
	}
	if app.Route() == nil {
		t.Fatal("App.Route 不应为空")
	}
	if app.Middleware() == nil {
		t.Fatal("App.Middleware 不应为空")
	}
	if app.Location().String() != "Asia/Shanghai" {
		t.Fatalf("ThinkPHP 默认时区应为 Asia/Shanghai，实际为 %q", app.Location())
	}
}

// TestAppPublicMethodsMatchThinkPHP 验证 think.App 中业务常用的调试、命名空间、
// 环境、路径、版本和运行上下文 API 保持相同名称及默认语义。
func TestAppPublicMethodsMatchThinkPHP(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app := NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })

	if app.Debug() != app || !app.IsDebug() {
		t.Fatal("Debug() 应开启调试模式并返回当前 App")
	}
	if app.Debug(false) != app || app.IsDebug() {
		t.Fatal("Debug(false) 应关闭调试模式并返回当前 App")
	}
	if app.DebugManager() == nil {
		t.Fatal("底层调试服务应通过 DebugManager 获取")
	}
	if app.SetNamespace("domain") != app || app.GetNamespace() != "domain" {
		t.Fatalf("应用命名空间 API 错误: %q", app.GetNamespace())
	}
	if got := app.ParseClass("controller", "admin/user_profile"); got != `domain\controller\admin\UserProfile` {
		t.Fatalf("ParseClass 结果错误: %q", got)
	}
	if app.SetBaseEnvName("base") != app || app.SetEnvName("testing") != app {
		t.Fatal("环境名称设置方法应返回当前 App")
	}
	if app.Version() != frameworkVersion.Number {
		t.Fatalf("框架版本错误: %q", app.Version())
	}
	if app.GetThinkPath() != filepath.Join(basePath, "framework") {
		t.Fatalf("框架目录错误: %q", app.GetThinkPath())
	}
	if app.GetConfigExt() != ".json" {
		t.Fatalf("配置扩展名错误: %q", app.GetConfigExt())
	}
	if !app.RunningInConsole() {
		t.Fatal("Console App 应识别为命令行运行环境")
	}
	if err := app.Initialize(); err != nil {
		t.Fatalf("初始化应用失败: %v", err)
	}
	if app.GetBeginTime() <= 0 || app.GetBeginMem() == 0 {
		t.Fatalf("初始化起点信息错误: time=%f memory=%d", app.GetBeginTime(), app.GetBeginMem())
	}
}

// TestAppLoadsBaseAndNamedEnvironmentLikeThinkPHP 验证公共环境文件先加载，
// 当前环境文件后加载并覆盖相同键。
func TestAppLoadsBaseAndNamedEnvironmentLikeThinkPHP(t *testing.T) {
	basePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(basePath, ".env.base"), []byte("APP_NAME=base\nSHARED=base\n"), 0o600); err != nil {
		t.Fatalf("写入公共环境文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(basePath, ".env.testing"), []byte("SHARED=testing\n"), 0o600); err != nil {
		t.Fatalf("写入当前环境文件失败: %v", err)
	}
	app := NewConsoleAppUninitialized(basePath).
		SetBaseEnvName("base").
		SetEnvName("testing")
	t.Cleanup(func() { _ = app.Close() })

	if err := app.LoadEnv("base"); err != nil {
		t.Fatalf("加载公共环境失败: %v", err)
	}
	if err := app.LoadEnv("testing"); err != nil {
		t.Fatalf("加载当前环境失败: %v", err)
	}
	if got := app.Env().Get("app_name"); got != "base" {
		t.Fatalf("公共环境值错误: %q", got)
	}
	if got := app.Env().Get("shared"); got != "testing" {
		t.Fatalf("当前环境未覆盖公共环境: %q", got)
	}
}
