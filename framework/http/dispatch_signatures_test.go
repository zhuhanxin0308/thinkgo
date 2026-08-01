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

	"thinkgo/framework"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/log"
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

type initErrorController struct{}

func (*initErrorController) Init(*framework.App, *fwcontext.Request) error {
	return errors.New("private init error")
}
func (*initErrorController) Show() string { return "unreachable" }

type invalidInitController struct{}

func (*invalidInitController) Init(*fwcontext.Request) {}
func (*invalidInitController) Show() string            { return "unreachable" }

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

// TestDispatchInitErrorsAndReservedAutoMethod 验证 Init 错误、非法签名和自动路由保留方法均在动作前阻断。
func TestDispatchInitErrorsAndReservedAutoMethod(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.BindFactory("InitError", func() interface{} { return &initErrorController{} })
	app.BindFactory("InvalidInit", func() interface{} { return &invalidInitController{} })
	handler := newTestHTTPHandler(t, app)
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/test", nil))
	for _, routeHandler := range []string{"InitError@Show", "InvalidInit@Show"} {
		response := handler.dispatch(routeForDispatchTest(t, routeHandler), req)
		if response.GetStatus() != http.StatusInternalServerError || strings.Contains(string(response.GetBody()), "private") {
			t.Fatalf("Init 失败必须返回脱敏 500，handler=%s status=%d body=%q", routeHandler, response.GetStatus(), string(response.GetBody()))
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
