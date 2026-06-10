package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"thinkgo/framework"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/exception"
	"thinkgo/framework/log"
	"thinkgo/framework/route"
)

type accessLogDriver struct {
	lock    sync.Mutex
	entries []*log.LogEntry
}

func (d *accessLogDriver) SaveEntries(entries []*log.LogEntry) error {
	d.lock.Lock()
	defer d.lock.Unlock()
	d.entries = append(d.entries, entries...)
	return nil
}

func (d *accessLogDriver) WriteEntry(entry *log.LogEntry) error {
	d.lock.Lock()
	defer d.lock.Unlock()
	d.entries = append(d.entries, entry)
	return nil
}

func (d *accessLogDriver) Close() error {
	return nil
}

func (d *accessLogDriver) allEntries() []*log.LogEntry {
	d.lock.Lock()
	defer d.lock.Unlock()
	return append([]*log.LogEntry(nil), d.entries...)
}

// TestServeHTTPLogsActualStatusAfterRecoveredException 验证访问日志记录的是最终真实响应码，
// 而不是 panic 分支里写死的 500。
func TestServeHTTPLogsActualStatusAfterRecoveredException(t *testing.T) {
	basePath := t.TempDir()
	app := newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false})
	driver := &accessLogDriver{}
	app.Log = log.NewLog(driver)
	app.Route.Get("/panic", func(req *fwcontext.Request) *fwcontext.Response {
		panic(exception.NewHttpException(http.StatusForbidden, "forbidden"))
	})

	handler := NewHttp(app)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/panic", nil)
	req.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)
	app.Log.Shutdown()

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("恢复后的最终响应状态码应为 403，实际为 %d", recorder.Code)
	}

	entries := driver.allEntries()
	if len(entries) == 0 {
		t.Fatal("应写入访问日志")
	}

	var accessEntry *log.LogEntry
	for _, entry := range entries {
		if entry.Level == "info" {
			accessEntry = entry
		}
	}
	if accessEntry == nil {
		t.Fatalf("应存在 info 级别访问日志，实际日志为 %#v", entries)
	}
	if got := accessEntry.Context["status"]; got != http.StatusForbidden {
		t.Fatalf("访问日志中的状态码应为 403，实际为 %#v", got)
	}
}

// TestDispatchMasksInternalErrorsOutsideDebug 验证生产模式下 dispatch 不会把内部容器错误明文返回给客户端。
func TestDispatchMasksInternalErrorsOutsideDebug(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.Container = framework.NewContainer()
	app.DebugMode = false

	handler := NewHttp(app)
	req := fwcontext.NewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/private", nil))
	resp := handler.dispatch(&route.Route{Handler: "MissingController@Show"}, req)

	if resp.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("缺失控制器时应返回 500，实际为 %d", resp.GetStatus())
	}
	if body := string(resp.GetBody()); body != "Internal Server Error" {
		t.Fatalf("生产模式不应泄露内部错误，实际响应为 %q", body)
	}
}

// TestDispatchShowsDetailedErrorsInDebug 验证调试模式下仍然会返回详细错误，便于定位问题。
func TestDispatchShowsDetailedErrorsInDebug(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.Container = framework.NewContainer()
	app.DebugMode = true

	handler := NewHttp(app)
	req := fwcontext.NewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/private", nil))
	resp := handler.dispatch(&route.Route{Handler: "MissingController@Show"}, req)

	if resp.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("缺失控制器时应返回 500，实际为 %d", resp.GetStatus())
	}
	if body := string(resp.GetBody()); !strings.Contains(body, "binding not found") {
		t.Fatalf("调试模式应保留详细错误，实际响应为 %q", body)
	}
}

// TestDispatchMasksMethodResolutionErrorsOutsideDebug 验证控制器方法解析失败时，生产模式同样不暴露内部细节。
func TestDispatchMasksMethodResolutionErrorsOutsideDebug(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.Container = framework.NewContainer()
	app.DebugMode = false
	app.BindFactory("BrokenController", func() interface{} {
		return &testDispatchController{}
	})

	handler := NewHttp(app)
	req := fwcontext.NewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/private", nil))
	resp := handler.dispatch(&route.Route{Handler: "BrokenController@Missing"}, req)

	if resp.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("缺失方法时应返回 500，实际为 %d", resp.GetStatus())
	}
	if body := string(resp.GetBody()); body != "Internal Server Error" {
		t.Fatalf("生产模式下方法解析错误不应泄露细节，实际响应为 %q", body)
	}
}
