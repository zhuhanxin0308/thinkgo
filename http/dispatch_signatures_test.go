package http

import (
	stdcontext "context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/log"
)

type dispatchErrorLogDriver struct {
	mu      sync.Mutex
	entries []*log.LogEntry
}

func (d *dispatchErrorLogDriver) SaveEntries(entries []*log.LogEntry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = append(d.entries, entries...)
	return nil
}

func (d *dispatchErrorLogDriver) WriteEntry(entry *log.LogEntry) error {
	return d.SaveEntries([]*log.LogEntry{entry})
}

func (d *dispatchErrorLogDriver) Close() error { return nil }

func (d *dispatchErrorLogDriver) snapshot() []*log.LogEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*log.LogEntry(nil), d.entries...)
}

type dispatchVariantsController struct{}

func (*dispatchVariantsController) Empty()       {}
func (*dispatchVariantsController) Text() string { return "text" }
func (*dispatchVariantsController) Data() map[string]interface{} {
	return map[string]interface{}{"ok": true}
}
func (*dispatchVariantsController) ResponseValue() fwcontext.Response {
	return *fwcontext.NewResponse().Code(http.StatusAccepted).Content("accepted")
}
func (*dispatchVariantsController) ErrorOnly() error                 { return nil }
func (*dispatchVariantsController) NilResponse() *fwcontext.Response { return nil }

type initializeErrorController struct{}

func (*initializeErrorController) Initialize() error {
	return errors.New("private initialize error")
}
func (*initializeErrorController) Show() string { return "unreachable" }

type invalidInitializeController struct{}

func (*invalidInitializeController) Initialize(*fwcontext.Request) {}
func (*invalidInitializeController) Show() string                  { return "unreachable" }

type initActionController struct {
	initCalls int
}

// Init 是普通业务动作，不是框架生命周期钩子。
func (controller *initActionController) Init() string {
	controller.initCalls++
	return "init-action"
}

func (controller *initActionController) Show() int {
	return controller.initCalls
}

// TestDispatchReturnVariants 验证控制器所有受支持返回形式都由签名计划稳定转换。
func TestDispatchReturnVariants(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.BindFactory("Variants", func() interface{} { return &dispatchVariantsController{} })
	handler := newTestHTTPHandler(t, app)
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/test", nil))

	tests := []struct {
		action string
		status int
		body   string
	}{
		{action: "Empty", status: http.StatusOK, body: ""},
		{action: "Text", status: http.StatusOK, body: "text"},
		{action: "Data", status: http.StatusOK, body: `{"ok":true}`},
		{action: "ResponseValue", status: http.StatusAccepted, body: "accepted"},
		{action: "ErrorOnly", status: http.StatusOK, body: ""},
		{action: "NilResponse", status: http.StatusOK, body: ""},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			response := handler.dispatch(routeForDispatchTest(t, "Variants@"+test.action), req)
			if response.GetStatus() != test.status || string(response.GetBody()) != test.body {
				t.Fatalf("返回值转换错误: status=%d body=%q", response.GetStatus(), string(response.GetBody()))
			}
		})
	}
}

// TestDispatchInitializeErrorsAndReservedAutoMethod 验证 Initialize 错误、非法签名和自动路由保留方法均在动作前阻断。
func TestDispatchInitializeErrorsAndReservedAutoMethod(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.BindFactory("InitializeError", func() interface{} { return &initializeErrorController{} })
	app.BindFactory("InvalidInitialize", func() interface{} { return &invalidInitializeController{} })
	handler := newTestHTTPHandler(t, app)
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/test", nil))
	for _, routeHandler := range []string{"InitializeError@Show", "InvalidInitialize@Show"} {
		response := handler.dispatch(routeForDispatchTest(t, routeHandler), req)
		if response.GetStatus() != http.StatusInternalServerError || strings.Contains(string(response.GetBody()), "private") {
			t.Fatalf("Initialize 失败必须返回脱敏 500，handler=%s status=%d body=%q", routeHandler, response.GetStatus(), string(response.GetBody()))
		}
	}

	autoRouter := mustHTTPRoute(t, app)
	if err := autoRouter.EnableAutoRoute(true); err != nil {
		t.Fatalf("启用自动路由失败: %v", err)
	}
	autoRequest := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/auto/view", nil))
	autoRoute, _, err := autoRouter.Match(autoRequest)
	if err != nil || autoRoute == nil {
		t.Fatalf("生成自动路由失败: route=%#v err=%v", autoRoute, err)
	}
	response := handler.dispatch(autoRoute, autoRequest)
	if response.GetStatus() != http.StatusNotFound {
		t.Fatalf("自动路由不得暴露基础控制器 View，实际状态为 %d", response.GetStatus())
	}
}

// TestDispatchTreatsInitAsBusinessAction 验证与 ThinkPHP 一样，Init 不承担
// App、Request 注入职责，也不会在其它动作前被框架隐式调用。
func TestDispatchTreatsInitAsBusinessAction(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.BindFactory("InitAction", func() interface{} { return &initActionController{} })
	handler := newTestHTTPHandler(t, app)
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/test", nil))

	showResponse := handler.dispatch(routeForDispatchTest(t, "InitAction@Show"), request)
	if showResponse.GetStatus() != http.StatusOK || string(showResponse.GetBody()) != "0" {
		t.Fatalf("普通动作前不得隐式执行 Init: status=%d body=%q", showResponse.GetStatus(), string(showResponse.GetBody()))
	}
	initResponse := handler.dispatch(routeForDispatchTest(t, "InitAction@Init"), request)
	if initResponse.GetStatus() != http.StatusOK || string(initResponse.GetBody()) != "init-action" {
		t.Fatalf("Init 应可作为显式业务动作: status=%d body=%q", initResponse.GetStatus(), string(initResponse.GetBody()))
	}
}

// TestDispatchInternalErrorRedactsSensitiveErrorText 验证控制器错误进入日志前不会泄露凭据。
func TestDispatchInternalErrorRedactsSensitiveErrorText(t *testing.T) {
	driver := &dispatchErrorLogDriver{}
	logger := log.NewLog(driver)
	logger.SetFlushInterval(time.Hour)
	handler := &Http{log: logger}
	secret := "dispatch-password-secret"
	handler.dispatchInternalError("controller", errors.New("database password="+secret))
	if err := logger.Flush(stdcontext.Background()); err != nil {
		t.Fatalf("刷新控制器错误日志失败: %v", err)
	}
	entries := driver.snapshot()
	if len(entries) != 1 {
		t.Fatalf("应记录一条控制器错误日志，实际为 %d", len(entries))
	}
	formatted := entries[0].FormatEntry()
	if strings.Contains(formatted, secret) || !strings.Contains(formatted, "[REDACTED]") {
		t.Fatalf("控制器错误日志必须脱敏凭据: %s", formatted)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭控制器错误日志失败: %v", err)
	}
}
