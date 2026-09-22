package http

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/event"
)

// TestHttpPublicConfigurationAPIMatchesThinkPHP 验证 Http 的应用名称、应用目录、
// 路由目录和绑定状态 API 与 ThinkPHP 使用相同名称及链式调用方式。
func TestHttpPublicConfigurationAPIMatchesThinkPHP(t *testing.T) {
	basePath := t.TempDir()
	app := framework.NewApp(basePath)
	t.Cleanup(func() { _ = app.Close() })
	httpKernel, err := NewHttp(app)
	if err != nil {
		t.Fatalf("构造 Http 失败: %v", err)
	}

	applicationPath := filepath.Join(basePath, "application")
	routePath := filepath.Join(basePath, "routes")
	if httpKernel.Name("index") != httpKernel || httpKernel.GetName() != "index" {
		t.Fatalf("Http.Name/GetName 错误: %q", httpKernel.GetName())
	}
	if httpKernel.Path(applicationPath) != httpKernel || httpKernel.GetPath() != filepath.Clean(applicationPath) {
		t.Fatalf("Http.Path/GetPath 错误: %q", httpKernel.GetPath())
	}
	httpKernel.SetRoutePath(routePath)
	if httpKernel.GetRoutePath() != filepath.Clean(routePath) {
		t.Fatalf("Http.SetRoutePath/GetRoutePath 错误: %q", httpKernel.GetRoutePath())
	}
	if httpKernel.SetBind() != httpKernel || !httpKernel.IsBind() {
		t.Fatal("Http.SetBind() 应启用绑定并返回当前 Http")
	}
	if httpKernel.SetBind(false) != httpKernel || httpKernel.IsBind() {
		t.Fatal("Http.SetBind(false) 应关闭绑定")
	}
}

// TestHttpDefaultsUseApplicationRoutePath 验证新 Http 默认使用根 route 目录。
func TestHttpDefaultsUseApplicationRoutePath(t *testing.T) {
	basePath := t.TempDir()
	app := framework.NewApp(basePath)
	t.Cleanup(func() { _ = app.Close() })
	httpKernel, err := NewHttp(app)
	if err != nil {
		t.Fatalf("构造 Http 失败: %v", err)
	}
	if httpKernel.GetName() != "" || httpKernel.GetPath() != "" {
		t.Fatalf("Http 名称和应用路径默认应为空: name=%q path=%q", httpKernel.GetName(), httpKernel.GetPath())
	}
	if httpKernel.GetRoutePath() != filepath.Join(basePath, "route") {
		t.Fatalf("Http 默认路由目录错误: %q", httpKernel.GetRoutePath())
	}
}

// TestHttpRunAndEndMatchThinkPHPLifecycle 验证 Http.Run 只执行一次请求并返回
// Response，响应发送后的 Http.End 再触发结束事件和中间件终结阶段。
func TestHttpRunAndEndMatchThinkPHPLifecycle(t *testing.T) {
	basePath := t.TempDir()
	ensureHTTPTestConfigFiles(t, basePath)
	app := framework.NewApp(basePath)
	t.Cleanup(func() { _ = app.Close() })

	events := make([]string, 0, 2)
	if err := app.Event().Listen(event.EventHttpRun, &event.SimpleListener{Handler: func(current event.Event) error {
		events = append(events, current.Name())
		return nil
	}}); err != nil {
		t.Fatalf("注册 HttpRun 监听器失败: %v", err)
	}
	if err := app.Event().Listen(event.EventHttpEnd, &event.SimpleListener{Handler: func(current event.Event) error {
		endEvent, ok := current.(*event.HttpEndEvent)
		if !ok || endEvent.StatusCode != http.StatusCreated || endEvent.Data == nil {
			t.Fatalf("HttpEnd 应携带完整响应: %#v", current)
		}
		events = append(events, current.Name())
		return nil
	}}); err != nil {
		t.Fatalf("注册 HttpEnd 监听器失败: %v", err)
	}
	if err := app.RegisterRouteLoader(func(application *framework.App) error {
		application.Route().Get("/orders/:id", func(request *fwcontext.Request) *fwcontext.Response {
			return fwcontext.NewResponse().Code(http.StatusCreated).Content(request.Route("id"))
		})
		return nil
	}); err != nil {
		t.Fatalf("注册测试路由失败: %v", err)
	}

	httpKernel, err := NewHttp(app)
	if err != nil {
		t.Fatalf("构造 Http 失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://localhost/orders/42", nil))
	response := httpKernel.Run(request)
	if response == nil || response.GetStatus() != http.StatusCreated || string(response.GetBody()) != "42" {
		t.Fatalf("Http.Run 响应错误: %#v", response)
	}
	if !reflect.DeepEqual(events, []string{event.EventHttpRun}) {
		t.Fatalf("Http.Run 不应提前执行 HttpEnd: %#v", events)
	}
	httpKernel.End(response)
	if !reflect.DeepEqual(events, []string{event.EventHttpRun, event.EventHttpEnd}) {
		t.Fatalf("Http.End 生命周期错误: %#v", events)
	}
}
