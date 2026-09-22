package http

import (
	stdcontext "context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

// TestHttpRunCreatesThinkPHPDefaultRequest 验证 Http.Run 省略 Request 时会创建
// 当前应用作用域内的默认 GET 请求，并把清理状态留给 Http.End。
func TestHttpRunCreatesThinkPHPDefaultRequest(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	router := mustHTTPRoute(t, application)
	registered, err := router.Get("/", func(request *fwcontext.Request) *fwcontext.Response {
		if request.Method() != http.MethodGet || request.Path() != "/" {
			return fwcontext.NewResponse().Code(http.StatusBadRequest).Content("unexpected default request")
		}
		return fwcontext.NewResponse().Content("default request")
	})
	if err != nil || registered == nil {
		t.Fatalf("注册默认请求路由失败: %v", err)
	}
	handler := newTestHTTPHandler(t, application)

	response := handler.Run()
	if response == nil || response.GetStatus() != http.StatusOK || string(response.GetBody()) != "default request" {
		t.Fatalf("默认请求响应错误: %#v", response)
	}
	if len(handler.endStates) != 1 {
		t.Fatalf("Http.Run 应保存一次请求结束状态，实际为 %d", len(handler.endStates))
	}
	handler.End(response)
	if len(handler.endStates) != 0 {
		t.Fatalf("Http.End 应消费请求结束状态，实际为 %d", len(handler.endStates))
	}

	badResponse := handler.Run(&fwcontext.Request{})
	if badResponse.GetStatus() != http.StatusBadRequest {
		t.Fatalf("缺少原始 URL 的请求应返回 400，实际为 %d", badResponse.GetStatus())
	}
	handler.End(badResponse)
}

// TestHttpEndUsesStableResponseIdentity 验证调用方复制 Response 值后，
// Http.End 仍能找到原请求的终结器和服务作用域，而不是依赖返回指针的内存地址。
func TestHttpEndUsesStableResponseIdentity(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	router := mustHTTPRoute(t, application)
	if _, err := router.Get("/identity", func() string { return "ok" }); err != nil {
		t.Fatalf("注册响应身份测试路由失败: %v", err)
	}
	handler := newTestHTTPHandler(t, application)
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://localhost/identity", nil))
	response := handler.Run(request)
	if len(handler.endStates) != 1 {
		t.Fatalf("Http.Run 应保存一次请求结束状态，实际为 %d", len(handler.endStates))
	}

	copied := *response
	handler.End(&copied)
	if len(handler.endStates) != 0 {
		t.Fatalf("复制后的 Response 应消费原请求结束状态，仍残留 %d 项", len(handler.endStates))
	}
}

// TestHttpRunBoundaryFailuresRemainStable 验证 Run 的参数数量、空内核和 panic
// 降级响应都不会把底层细节暴露给业务调用者。
func TestHttpRunBoundaryFailuresRemainStable(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	handler := newTestHTTPHandler(t, application)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Http.Run 接收两个 Request 时必须 panic")
			}
		}()
		handler.Run(nil, nil)
	}()

	var nilHandler *Http
	response := nilHandler.Run()
	if response.GetStatus() != http.StatusInternalServerError || response.GetHeader("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("空 Http 应返回安全的 500 响应: %#v", response)
	}
	nilHandler.End(response)
	handler.End(nil)
}

// TestResponseRecorderConversionFiltersHostHeaders 验证异常渲染结果转换为框架 Response 时，
// 会保留业务响应头的多值顺序并过滤只能由 net/http 宿主管理的响应头。
func TestResponseRecorderConversionFiltersHostHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Code = http.StatusCreated
	recorder.Header().Add("X-Test", "first")
	recorder.Header().Add("X-Test", "second")
	recorder.Header().Set("Connection", "close")
	recorder.Header().Set("Content-Length", "999")
	recorder.Body.WriteString("created")

	response := responseFromHTTPRecorder(recorder)
	if response.GetStatus() != http.StatusCreated || string(response.GetBody()) != "created" {
		t.Fatalf("Recorder 响应转换错误: %#v", response)
	}
	if values := response.Headers().Values("X-Test"); len(values) != 2 || values[0] != "first" || values[1] != "second" {
		t.Fatalf("业务响应头多值丢失: %#v", values)
	}
	if response.GetHeader("Connection") != "" || response.GetHeader("Content-Length") != "" {
		t.Fatalf("宿主管理响应头不应进入框架 Response: %#v", response.Headers())
	}
	if fallback := responseFromHTTPRecorder(nil); fallback.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("空 Recorder 应返回 500，实际为 %d", fallback.GetStatus())
	}
	for _, header := range []string{"Connection", "content-length", "Keep-Alive", "Proxy-Connection", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		if !isHostManagedHeader(header) {
			t.Fatalf("%q 应由 HTTP 宿主管理", header)
		}
	}
	if isHostManagedHeader("X-Business") {
		t.Fatal("业务响应头不应被识别为宿主管理头")
	}
}

// TestHTTPServerFacadeLifecycleBoundaries 验证 Server 宿主只委托 Http 监听，
// 并在 nil、已取消上下文和无效底层处理器处返回可判断错误。
func TestHTTPServerFacadeLifecycleBoundaries(t *testing.T) {
	var nilServer *Server
	if err := nilServer.Run(); err == nil {
		t.Fatal("空 Server.Run 应返回初始化错误")
	}
	if err := nilServer.RunContext(stdcontext.Background()); err == nil {
		t.Fatal("空 Server.RunContext 应返回初始化错误")
	}
	emptyServer := NewServer(nil)
	if emptyServer == nil || emptyServer.http != nil {
		t.Fatalf("NewServer(nil) 元数据错误: %#v", emptyServer)
	}
	if err := emptyServer.Run(); err == nil {
		t.Fatal("未绑定 Http 的 Server.Run 应失败")
	}

	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	handler := newTestHTTPHandler(t, application)
	server := NewServer(handler)
	cancelled, cancel := stdcontext.WithCancel(stdcontext.Background())
	cancel()
	if err := server.RunContext(cancelled); !errors.Is(err, stdcontext.Canceled) {
		t.Fatalf("Server.RunContext 应保留取消原因，实际为 %v", err)
	}
	if err := runHTTPServers(nil, handler.srvConf, nil, nil); err == nil || !strings.Contains(err.Error(), "处理器") {
		t.Fatalf("runHTTPServers 应拒绝空处理器，实际为 %v", err)
	}

	if built := handler.newServer(); built == nil || built.Handler != handler || built.Addr == "" {
		t.Fatalf("Http.newServer 配置错误: %#v", built)
	}
	if built := handler.newHTTP3Server("127.0.0.1:0"); built == nil || built.Handler != handler || built.Addr != "127.0.0.1:0" {
		t.Fatalf("Http.newHTTP3Server 配置错误: %#v", built)
	}
	if err := handler.shutdownServers(nil, nil); err != nil {
		t.Fatalf("关闭空服务集合应成功: %v", err)
	}
	var nilHTTP *Http
	if nilHTTP.newServer() != nil || nilHTTP.newHTTP3Server("127.0.0.1:0") != nil {
		t.Fatal("空 Http 不应创建服务实例")
	}
	if err := nilHTTP.shutdownServers(nil, nil); err == nil {
		t.Fatal("空 Http 关闭服务应返回初始化错误")
	}
	if err := shutdownConfiguredServers(nil, nil, time.Second); err != nil {
		t.Fatalf("关闭空配置服务集合应成功: %v", err)
	}
}
