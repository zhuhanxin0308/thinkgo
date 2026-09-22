package http

import (
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
)

// TestHttpNativelyDispatchesMultipleApplications 验证普通 Http 入口原生完成
// 域名、映射和路径应用选择，不需要额外 MultiHttp 或 ApplicationManager。
func TestHttpNativelyDispatchesMultipleApplications(t *testing.T) {
	basePath := t.TempDir()
	writeNativeHTTPConfig(t, basePath, `{
  "app_env":"test",
  "default_app":"index",
  "app_map":{"backend":"admin"},
  "domain_bind":{"admin.example.com":"admin"},
  "deny_app_list":["blocked"],
  "server":{"host":"127.0.0.1","port":8080},
  "compression":{"enable":false}
}`)

	application := framework.NewConsoleAppUninitialized(basePath)
	if err := application.RegisterApplications(
		nativeHTTPGlobalLoader,
		framework.ApplicationDefinition{Name: "index", Register: nativeHTTPApplicationLoader("index")},
		framework.ApplicationDefinition{Name: "admin", Register: nativeHTTPApplicationLoader("admin")},
		framework.ApplicationDefinition{Name: "blocked", Register: nativeHTTPApplicationLoader("blocked")},
	); err != nil {
		t.Fatalf("注册 HTTP 多应用失败: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })

	kernel, err := NewHttp(application)
	if err != nil {
		t.Fatalf("创建原生多应用 HTTP 内核失败: %v", err)
	}
	tests := []struct {
		name       string
		host       string
		path       string
		status     int
		body       string
		globalPath string
		appPath    string
	}{
		{name: "named index", host: "www.example.com", path: "/index/who", status: 200, body: "index|index|/index|who", globalPath: "index/who", appPath: "who"},
		{name: "mapped admin", host: "www.example.com", path: "/backend/who", status: 200, body: "admin|admin|/backend|who", globalPath: "backend/who", appPath: "who"},
		{name: "domain admin", host: "admin.example.com", path: "/who", status: 200, body: "admin|admin||who", globalPath: "who", appPath: "who"},
		{name: "denied", host: "www.example.com", path: "/blocked/who", status: 404},
		{name: "unknown", host: "www.example.com", path: "/missing/who", status: 404},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(stdhttp.MethodGet, "http://"+test.host+test.path, nil)
			request.Host = test.host
			recorder := httptest.NewRecorder()
			kernel.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("状态码错误: got %d body=%q", recorder.Code, recorder.Body.String())
			}
			if test.body != "" && recorder.Body.String() != test.body {
				t.Errorf("响应应用错误: got %q want %q", recorder.Body.String(), test.body)
			}
			if test.globalPath != "" && recorder.Header().Get("X-Global-Path") != test.globalPath {
				t.Errorf("全局中间件看到的路径错误: %q", recorder.Header().Get("X-Global-Path"))
			}
			if test.appPath != "" && recorder.Header().Get("X-Application-Path") != test.appPath {
				t.Errorf("应用中间件看到的路径错误: %q", recorder.Header().Get("X-Application-Path"))
			}
		})
	}
}

// TestHttpOneApplicationBehavesAsSingleApplication 验证只声明 index 时，业务
// 路由既可直接访问，也可使用显式应用前缀，框架无需切换运行模式。
func TestHttpOneApplicationBehavesAsSingleApplication(t *testing.T) {
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
	application := framework.NewConsoleAppUninitialized(basePath)
	if err := application.RegisterApplications(
		nativeHTTPGlobalLoader,
		framework.ApplicationDefinition{Name: "index", Register: nativeHTTPApplicationLoader("index")},
	); err != nil {
		t.Fatalf("注册单一原生应用失败: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	kernel, err := NewHttp(application)
	if err != nil {
		t.Fatalf("创建单一应用 HTTP 内核失败: %v", err)
	}

	for _, path := range []string{"/who", "/index/who"} {
		recorder := httptest.NewRecorder()
		kernel.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com"+path, nil))
		if recorder.Code != stdhttp.StatusOK || recorder.Body.String() == "" {
			t.Errorf("单一应用路径 %q 未按同一内核处理: status=%d body=%q", path, recorder.Code, recorder.Body.String())
		}
	}
}

// TestHttpResolvesApplicationFromProjectConfigSnapshot 验证当前应用自己的
// app.json 可以覆盖业务配置，但不能反向改变本次请求之前确定的多应用解析规则。
func TestHttpResolvesApplicationFromProjectConfigSnapshot(t *testing.T) {
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
	indexConfigPath := filepath.Join(basePath, "app", "index", "config")
	if err := os.MkdirAll(indexConfigPath, 0o755); err != nil {
		t.Fatalf("创建 index 配置目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(indexConfigPath, "app.json"), []byte(`{"default_app":"admin","application_marker":"index-overlay"}`), 0o644); err != nil {
		t.Fatalf("写入 index 应用配置失败: %v", err)
	}
	application := framework.NewConsoleAppUninitialized(basePath)
	if err := application.RegisterApplications(
		nativeHTTPGlobalLoader,
		framework.ApplicationDefinition{Name: "index", Register: nativeHTTPApplicationLoader("index")},
		framework.ApplicationDefinition{Name: "admin", Register: nativeHTTPApplicationLoader("admin")},
	); err != nil {
		t.Fatalf("注册配置隔离测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	kernel, err := NewHttp(application)
	if err != nil {
		t.Fatalf("创建配置隔离 HTTP 内核失败: %v", err)
	}
	recorder := httptest.NewRecorder()
	kernel.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/", nil))
	if recorder.Code != stdhttp.StatusOK || recorder.Body.String() != "index" {
		t.Fatalf("默认应用被应用级配置污染: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := application.Config().GetString("app.application_marker"); got != "index-overlay" {
		t.Fatalf("index 应用配置覆盖未生效: %q", got)
	}
}

// TestNativeApplicationEventsMatchThinkPHPMultiAppOrder 验证 HttpRun 只触发
// 项目级监听器，而应用级监听器从路由阶段开始生效并能接收 HttpEnd。
func TestNativeApplicationEventsMatchThinkPHPMultiAppOrder(t *testing.T) {
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
	var lock sync.Mutex
	sequence := make([]string, 0, 4)
	record := func(marker string) event.Listener {
		return &event.SimpleListener{Handler: func(event.Event) error {
			lock.Lock()
			sequence = append(sequence, marker)
			lock.Unlock()
			return nil
		}}
	}
	application := framework.NewConsoleAppUninitialized(basePath)
	if err := application.RegisterApplications(
		func(current *framework.App) error {
			return current.LoadEvent(framework.EventDefinition{Listen: map[string][]event.Listener{
				event.EventHttpRun: {record("global-run")},
				event.EventHttpEnd: {record("global-end")},
			}})
		},
		framework.ApplicationDefinition{Name: "index", Register: func(current *framework.App) error {
			if err := current.LoadEvent(framework.EventDefinition{Listen: map[string][]event.Listener{
				event.EventHttpRun: {record("application-run")},
				event.EventHttpEnd: {record("application-end")},
			}}); err != nil {
				return err
			}
			return current.RegisterRouteLoader(func(routeApplication *framework.App) error {
				routeApplication.Route().Get("/", func() string {
					lock.Lock()
					sequence = append(sequence, "route")
					lock.Unlock()
					return "ok"
				})
				return nil
			})
		}},
	); err != nil {
		t.Fatalf("注册生命周期测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	kernel, err := NewHttp(application)
	if err != nil {
		t.Fatalf("创建生命周期测试内核失败: %v", err)
	}
	recorder := httptest.NewRecorder()
	kernel.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/", nil))
	if recorder.Code != stdhttp.StatusOK || recorder.Body.String() != "ok" {
		t.Fatalf("生命周期请求失败: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	lock.Lock()
	actual := append([]string(nil), sequence...)
	lock.Unlock()
	expected := []string{"global-run", "route", "global-end", "application-end"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("多应用事件顺序错误: got=%#v want=%#v", actual, expected)
	}
}

// TestNativeHttpRunAndEndDelegateToSelectedApplication 验证直接使用 ThinkPHP
// 风格 Http.Run/End 时，多应用宿主仍把请求和结束阶段交给同一个应用内核。
func TestNativeHttpRunAndEndDelegateToSelectedApplication(t *testing.T) {
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
	ended := 0
	application := framework.NewConsoleAppUninitialized(basePath)
	if err := application.RegisterApplications(
		func(current *framework.App) error {
			return current.LoadEvent(framework.EventDefinition{Listen: map[string][]event.Listener{
				event.EventHttpEnd: {&event.SimpleListener{Handler: func(event.Event) error {
					ended++
					return nil
				}}},
			}})
		},
		framework.ApplicationDefinition{Name: "index", Register: func(current *framework.App) error {
			return current.RegisterRouteLoader(func(routeApplication *framework.App) error {
				routeApplication.Route().Get("/", func(request *fwcontext.Request) string {
					applicationContext, _ := request.ApplicationContext()
					return applicationContext.Name()
				})
				return nil
			})
		}},
	); err != nil {
		t.Fatalf("注册 Run/End 测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	kernel, err := NewHttp(application)
	if err != nil {
		t.Fatalf("创建 Run/End 测试内核失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(stdhttp.MethodGet, "http://example.com/", nil))
	response := kernel.Run(request)
	if response == nil || response.GetStatus() != stdhttp.StatusOK || response.GetContent() != "index" {
		t.Fatalf("原生多应用 Http.Run 响应错误: %#v", response)
	}
	if ended != 0 {
		t.Fatalf("Http.Run 不得提前触发 HttpEnd: %d", ended)
	}
	copied := *response
	kernel.End(&copied)
	if ended != 1 {
		t.Fatalf("Http.End 未委托到 index 应用: %d", ended)
	}
	if child := kernel.applicationHost.applications["index"]; len(child.endStates) != 0 {
		t.Fatalf("复制后的 Response 应消费子应用结束状态，仍残留 %d 项", len(child.endStates))
	}
}

type directRunScopeProbe struct {
	closed int
}

func (*directRunScopeProbe) Make(string, ...interface{}) (interface{}, error) {
	return "caller", nil
}

func (probe *directRunScopeProbe) Close() error {
	probe.closed++
	return nil
}

// TestNativeHttpRunRebindsSelectedApplicationScope 验证调用方传入的 Request
// 会切换到目标应用作用域，同时解析失败和正常结束都不会泄漏原作用域。
func TestNativeHttpRunRebindsSelectedApplicationScope(t *testing.T) {
	basePath := t.TempDir()
	writeNativeHTTPConfig(t, basePath, `{
  "app_env":"test",
  "default_app":"index",
  "app_map":{"backend":"admin"},
  "domain_bind":{},
  "deny_app_list":[],
  "server":{"host":"127.0.0.1","port":8080},
  "compression":{"enable":false}
}`)
	application := framework.NewConsoleAppUninitialized(basePath)
	if err := application.RegisterApplications(
		nativeHTTPGlobalLoader,
		framework.ApplicationDefinition{Name: "index", Register: nativeHTTPApplicationLoader("index")},
		framework.ApplicationDefinition{Name: "admin", Register: nativeHTTPApplicationLoader("admin")},
	); err != nil {
		t.Fatalf("注册作用域测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	kernel, err := NewHttp(application)
	if err != nil {
		t.Fatalf("创建作用域测试内核失败: %v", err)
	}

	callerScope := &directRunScopeProbe{}
	request, err := fwcontext.NewRequest(
		httptest.NewRequest(stdhttp.MethodGet, "http://example.com/backend/scope", nil),
		fwcontext.WithServiceScope(callerScope, callerScope),
	)
	if err != nil {
		t.Fatalf("创建调用方请求失败: %v", err)
	}
	response := kernel.Run(request)
	if response == nil || response.GetStatus() != stdhttp.StatusOK || response.GetContent() != "admin" {
		t.Fatalf("直接运行未切换到 admin 作用域: %#v", response)
	}
	if callerScope.closed != 0 {
		t.Fatalf("Http.Run 不得提前关闭调用方作用域: %d", callerScope.closed)
	}
	kernel.End(response)
	if callerScope.closed != 1 {
		t.Fatalf("Http.End 必须关闭调用方作用域一次: %d", callerScope.closed)
	}

	missingScope := &directRunScopeProbe{}
	missingRequest, err := fwcontext.NewRequest(
		httptest.NewRequest(stdhttp.MethodGet, "http://example.com/missing/scope", nil),
		fwcontext.WithServiceScope(missingScope, missingScope),
	)
	if err != nil {
		t.Fatalf("创建未命中应用请求失败: %v", err)
	}
	missingResponse := kernel.Run(missingRequest)
	if missingResponse == nil || missingResponse.GetStatus() != stdhttp.StatusNotFound {
		t.Fatalf("不存在的应用必须返回 404: %#v", missingResponse)
	}
	kernel.End(missingResponse)
	if missingScope.closed != 1 {
		t.Fatalf("应用解析失败后必须关闭调用方作用域一次: %d", missingScope.closed)
	}
}

// TestNativeApplicationsRemainIsolatedAcrossConcurrentRequests 验证不同应用的
// 路径、容器、路由和请求上下文不会因常驻并发服务而交叉污染。
func TestNativeApplicationsRemainIsolatedAcrossConcurrentRequests(t *testing.T) {
	basePath := t.TempDir()
	writeNativeHTTPConfig(t, basePath, `{
  "app_env":"test",
  "default_app":"index",
  "app_map":{"backend":"admin"},
  "domain_bind":{},
  "deny_app_list":[],
  "server":{"host":"127.0.0.1","port":8080},
  "compression":{"enable":false}
}`)
	application := framework.NewConsoleAppUninitialized(basePath)
	if err := application.RegisterApplications(
		nativeHTTPGlobalLoader,
		framework.ApplicationDefinition{Name: "index", Register: nativeHTTPApplicationLoader("index")},
		framework.ApplicationDefinition{Name: "admin", Register: nativeHTTPApplicationLoader("admin")},
	); err != nil {
		t.Fatalf("注册并发测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	kernel, err := NewHttp(application)
	if err != nil {
		t.Fatalf("创建并发测试内核失败: %v", err)
	}

	const requestCount = 96
	errorsByRequest := make(chan error, requestCount)
	var waitGroup sync.WaitGroup
	for index := 0; index < requestCount; index++ {
		waitGroup.Add(1)
		go func(current int) {
			defer waitGroup.Done()
			path := "/index/who"
			expected := "index|index|/index|who"
			if current%2 == 1 {
				path = "/backend/who"
				expected = "admin|admin|/backend|who"
			}
			recorder := httptest.NewRecorder()
			kernel.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com"+path, nil))
			if recorder.Code != stdhttp.StatusOK || recorder.Body.String() != expected {
				errorsByRequest <- fmt.Errorf("请求 %d 路径 %s: status=%d body=%q", current, path, recorder.Code, recorder.Body.String())
			}
		}(index)
	}
	waitGroup.Wait()
	close(errorsByRequest)
	for requestErr := range errorsByRequest {
		t.Error(requestErr)
	}
}

// TestNativeApplicationsFailBeforeServing 验证所有编译期应用都会在宿主开始
// 提供服务前完成初始化；任一应用损坏时必须整体失败，不能留下部分可用实例。
func TestNativeApplicationsFailBeforeServing(t *testing.T) {
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
	adminConfigDirectory := filepath.Join(basePath, "app", "admin", "config")
	if err := os.MkdirAll(adminConfigDirectory, 0o755); err != nil {
		t.Fatalf("创建 admin 配置目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(adminConfigDirectory, "app.json"), []byte(`{"broken":`), 0o644); err != nil {
		t.Fatalf("写入损坏的 admin 配置失败: %v", err)
	}
	application := framework.NewConsoleAppUninitialized(basePath)
	if err := application.RegisterApplications(
		nativeHTTPGlobalLoader,
		framework.ApplicationDefinition{Name: "index", Register: nativeHTTPApplicationLoader("index")},
		framework.ApplicationDefinition{Name: "admin", Register: nativeHTTPApplicationLoader("admin")},
	); err != nil {
		t.Fatalf("注册启动失败测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	kernel, err := NewHttp(application)
	if err != nil {
		t.Fatalf("创建启动失败测试内核失败: %v", err)
	}
	if err := kernel.ensureInitialized(); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("损坏的 admin 必须在宿主服务前阻断启动并标明应用: %v", err)
	}
}

// TestNativeHostUsesProjectListenerConfiguration 验证应用级 server 配置只影响
// 该应用的请求处理器，不能覆盖项目入口实际监听的地址、端口和协议配置。
func TestNativeHostUsesProjectListenerConfiguration(t *testing.T) {
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
	indexConfigPath := filepath.Join(basePath, "app", "index", "config")
	if err := os.MkdirAll(indexConfigPath, 0o755); err != nil {
		t.Fatalf("创建 index 配置目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(indexConfigPath, "app.json"), []byte(`{"server":{"host":"127.0.0.1","port":9090},"compression":{"enable":false}}`), 0o644); err != nil {
		t.Fatalf("写入 index 应用监听覆盖配置失败: %v", err)
	}
	application := framework.NewConsoleAppUninitialized(basePath)
	if err := application.RegisterApplications(
		nativeHTTPGlobalLoader,
		framework.ApplicationDefinition{Name: "index", Register: nativeHTTPApplicationLoader("index")},
	); err != nil {
		t.Fatalf("注册监听配置隔离应用失败: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	kernel, err := NewHttp(application)
	if err != nil {
		t.Fatalf("创建监听配置隔离内核失败: %v", err)
	}
	if err := kernel.ensureInitialized(); err != nil {
		t.Fatalf("初始化监听配置隔离内核失败: %v", err)
	}
	if kernel.srvConf.Port != 8080 {
		t.Fatalf("项目监听端口被 index 应用覆盖: got=%d want=8080", kernel.srvConf.Port)
	}
	indexHandler := kernel.applicationHost.applications["index"]
	if indexHandler == nil || indexHandler.srvConf.Port != 9090 {
		t.Fatalf("index 请求处理器未保留应用级配置: %#v", indexHandler)
	}
}

// TestNativeHttpNameAndPathBindCompiledApplication 验证 ThinkPHP 的
// Http.Name().Path() 调用方式会把显式入口绑定到指定应用及其自定义目录。
func TestNativeHttpNameAndPathBindCompiledApplication(t *testing.T) {
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
	customPath := filepath.Join(basePath, "applications", "admin")
	if err := os.MkdirAll(filepath.Join(customPath, "config"), 0o755); err != nil {
		t.Fatalf("创建自定义应用目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(customPath, "config", "app.json"), []byte(`{"application_marker":"custom-admin"}`), 0o644); err != nil {
		t.Fatalf("写入自定义应用配置失败: %v", err)
	}
	application := framework.NewConsoleAppUninitialized(basePath)
	if err := application.RegisterApplications(
		func(*framework.App) error { return nil },
		framework.ApplicationDefinition{Name: "index", Register: nativeHTTPApplicationLoader("index")},
		framework.ApplicationDefinition{Name: "admin", Register: func(current *framework.App) error {
			return current.RegisterRouteLoader(func(routeApplication *framework.App) error {
				routeApplication.Route().Get("where", func() string {
					return routeApplication.Config().GetString("app.application_marker") + "|" + routeApplication.GetAppPath()
				})
				return nil
			})
		}},
	); err != nil {
		t.Fatalf("注册自定义路径应用失败: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	kernel, err := NewHttp(application)
	if err != nil {
		t.Fatalf("创建自定义路径 HTTP 内核失败: %v", err)
	}
	kernel.Name("admin").Path(customPath)
	recorder := httptest.NewRecorder()
	kernel.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/where", nil))
	expected := "custom-admin|" + customPath
	if recorder.Code != stdhttp.StatusOK || recorder.Body.String() != expected {
		t.Fatalf("Name/Path 显式绑定错误: status=%d body=%q want=%q", recorder.Code, recorder.Body.String(), expected)
	}
}

func nativeHTTPGlobalLoader(current *framework.App) error {
	return current.RegisterGlobalMiddleware(func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		path := request.Pathinfo()
		response := next(request)
		response.Header("X-Global-Path", path)
		return response
	})
}

func nativeHTTPApplicationLoader(name string) framework.ApplicationLoader {
	return func(current *framework.App) error {
		if err := current.Instance("native.application.identity", name); err != nil {
			return err
		}
		if err := current.RegisterApplicationMiddleware(func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			path := request.Pathinfo()
			response := next(request)
			response.Header("X-Application-Path", path)
			return response
		}); err != nil {
			return err
		}
		return current.RegisterRouteLoader(func(routeApplication *framework.App) error {
			routeApplication.Route().Get("/", func() string { return name })
			routeApplication.Route().Get("who", func(request *fwcontext.Request) string {
				applicationContext, _ := request.ApplicationContext()
				return name + "|" + applicationContext.Name() + "|" + request.Root() + "|" + request.Pathinfo()
			})
			routeApplication.Route().Get("scope", func(request *fwcontext.Request) string {
				value, err := request.Make("native.application.identity")
				if err != nil {
					return "error:" + err.Error()
				}
				return fmt.Sprint(value)
			})
			return nil
		})
	}
}

func writeNativeHTTPConfig(t *testing.T, basePath, appConfig string) {
	t.Helper()
	ensureHTTPTestConfigFiles(t, basePath)
	if err := os.WriteFile(filepath.Join(basePath, "config", "app.json"), []byte(appConfig), 0o644); err != nil {
		t.Fatalf("写入多应用 HTTP 配置失败: %v", err)
	}
	for _, name := range []string{"index", "admin", "blocked"} {
		if err := os.MkdirAll(filepath.Join(basePath, "app", name), 0o755); err != nil {
			t.Fatalf("创建应用目录 %s 失败: %v", name, err)
		}
	}
}
