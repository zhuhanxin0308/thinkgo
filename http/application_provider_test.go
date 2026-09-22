package http

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

type applicationExceptionHandlerProbe struct {
	reports atomic.Int32
	renders atomic.Int32
}

// Report 记录应用异常处理器收到的报告阶段。
func (handler *applicationExceptionHandlerProbe) Report(interface{}) {
	handler.reports.Add(1)
}

// Render 输出可识别的应用级异常响应。
func (handler *applicationExceptionHandlerProbe) Render(writer http.ResponseWriter, _ *http.Request, _ interface{}) error {
	handler.renders.Add(1)
	writer.WriteHeader(http.StatusTeapot)
	_, err := writer.Write([]byte("application exception"))
	return err
}

// TestHttpUsesApplicationRequestAndExceptionProviderBindings 验证 app/provider.go 对应的
// request 与 exception_handle 绑定会进入真实 HTTP 请求和异常生命周期。
func TestHttpUsesApplicationRequestAndExceptionProviderBindings(t *testing.T) {
	basePath := t.TempDir()
	ensureHTTPTestConfigFiles(t, basePath)
	application := framework.NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = application.Close() })

	var requestCreations atomic.Int32
	requestFactory := func(raw *http.Request, options ...fwcontext.RequestOption) (*fwcontext.Request, error) {
		requestCreations.Add(1)
		request, err := fwcontext.NewRequest(raw, options...)
		if err == nil {
			request.Set("application_request", "ready")
		}
		return request, err
	}
	exceptionHandler := &applicationExceptionHandlerProbe{}
	if err := application.BindFactory(string(framework.ServiceRequest), requestFactory); err != nil {
		t.Fatalf("绑定应用请求工厂失败: %v", err)
	}
	if err := application.Instance(string(framework.ServiceExceptionHandle), exceptionHandler); err != nil {
		t.Fatalf("绑定应用异常处理器失败: %v", err)
	}
	if err := application.RegisterRouteLoader(func(current *framework.App) error {
		current.Route().Get("/request-provider", func(request *fwcontext.Request) *fwcontext.Response {
			return fwcontext.NewResponse().Content(request.GetData("application_request").(string))
		})
		current.Route().Get("/exception-provider", func(*fwcontext.Request) *fwcontext.Response {
			panic("provider panic")
		})
		return nil
	}); err != nil {
		t.Fatalf("注册 Provider 测试路由失败: %v", err)
	}
	if err := application.Initialize(); err != nil {
		t.Fatalf("初始化 Provider 测试应用失败: %v", err)
	}

	handler, err := NewHttp(application)
	if err != nil {
		t.Fatalf("创建 HTTP 内核失败: %v", err)
	}
	requestRecorder := httptest.NewRecorder()
	handler.ServeHTTP(requestRecorder, httptest.NewRequest(http.MethodGet, "http://localhost/request-provider", nil))
	if requestRecorder.Code != http.StatusOK || requestRecorder.Body.String() != "ready" {
		t.Fatalf("应用请求工厂未生效: status=%d body=%q", requestRecorder.Code, requestRecorder.Body.String())
	}

	exceptionRecorder := httptest.NewRecorder()
	handler.ServeHTTP(exceptionRecorder, httptest.NewRequest(http.MethodGet, "http://localhost/exception-provider", nil))
	if exceptionRecorder.Code != http.StatusTeapot || exceptionRecorder.Body.String() != "application exception" {
		t.Fatalf("应用异常处理器未生效: status=%d body=%q", exceptionRecorder.Code, exceptionRecorder.Body.String())
	}
	if requestCreations.Load() != 2 {
		t.Fatalf("每个请求必须创建独立应用 Request，实际 %d 次", requestCreations.Load())
	}
	if exceptionHandler.reports.Load() != 1 || exceptionHandler.renders.Load() != 1 {
		t.Fatalf("异常应依次报告和渲染一次: report=%d render=%d", exceptionHandler.reports.Load(), exceptionHandler.renders.Load())
	}
}
