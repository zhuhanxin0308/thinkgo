package http

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"thinkgo/framework"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/event"
	"thinkgo/framework/exception"
	"thinkgo/framework/log"
	"thinkgo/framework/route"
)

// TestServeHTTPTurnsHttpRunListenerErrorInto500 验证请求开始事件错误进入统一异常处理而非继续执行业务路由。
func TestServeHTTPTurnsHttpRunListenerErrorInto500(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.Event = event.NewDispatcher()
	listenerErr := errors.New("private listener failure")
	if err := app.Event.Listen(event.EventHttpRun, &event.SimpleListener{Handler: func(event.Event) error {
		return listenerErr
	}}); err != nil {
		t.Fatalf("注册请求事件监听器失败: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	request.Header.Set("Accept", "application/json")
	newTestHTTPHandler(t, app).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("请求事件失败应返回 500，实际为 %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), listenerErr.Error()) {
		t.Fatalf("生产响应不应泄露监听器错误，实际为 %q", recorder.Body.String())
	}
}

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

	handler := newTestHTTPHandler(t, app)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/panic", nil)
	req.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)
	_ = app.Log.Close()

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

// TestServeHTTPDiscardsUncommittedCompressedBodyOnPanic 验证缓冲中的业务片段不会与异常响应拼接或保留原状态码。
func TestServeHTTPDiscardsUncommittedCompressedBodyOnPanic(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{
		"enable":   true,
		"min_size": 1024,
		"levels":   map[string]interface{}{"gzip": 1},
	})
	if _, err := app.Route.Get("/stream-panic", func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Stream(func(writer io.Writer) error {
			_, _ = writer.Write([]byte("private-partial-body"))
			panic(exception.NewHttpException(http.StatusInternalServerError, "failed"))
		})
	}); err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/stream-panic", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()
	newTestHTTPHandler(t, app).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("未提交响应发生 panic 后应返回 500，实际为 %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "private-partial-body") {
		t.Fatalf("异常响应不得包含此前缓冲的业务数据，实际为 %q", recorder.Body.String())
	}
}

// TestServeHTTPDiscardsUncommittedBodyOnStreamError 验证流回调返回错误时，未提交的缓冲内容会被通用 500 原子替换。
func TestServeHTTPDiscardsUncommittedBodyOnStreamError(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{
		"enable":   true,
		"min_size": 1024,
		"levels":   map[string]interface{}{"gzip": 1},
	})
	streamErr := errors.New("private stream error")
	if _, err := app.Route.Get("/stream-error", func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Stream(func(writer io.Writer) error {
			_, _ = writer.Write([]byte("private-partial-body"))
			return streamErr
		})
	}); err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/stream-error", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	newTestHTTPHandler(t, app).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("未提交流错误应返回 500，实际为 %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "private-partial-body") || strings.Contains(recorder.Body.String(), streamErr.Error()) {
		t.Fatalf("流错误响应不得泄露部分数据或内部错误: %q", recorder.Body.String())
	}
}

// TestServeHTTPCanReplaceEmptyUncompressedStreamError 验证流在写出前失败时，即使未启用压缩也仍可返回 500。
func TestServeHTTPCanReplaceEmptyUncompressedStreamError(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if _, err := app.Route.Get("/empty-stream-error", func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Stream(func(io.Writer) error {
			return errors.New("stream failed before write")
		})
	}); err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}
	recorder := httptest.NewRecorder()
	newTestHTTPHandler(t, app).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://example.com/empty-stream-error", nil))
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "stream failed before write") {
		t.Fatalf("写出前流错误应安全返回 500，状态=%d，响应=%q", recorder.Code, recorder.Body.String())
	}
}

// TestServeHTTPPreservesIntentionalResponseBuildErrors 验证安全下载拒绝等已构造响应不会被发送错误兜底误改为 500。
func TestServeHTTPPreservesIntentionalResponseBuildErrors(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{
		"enable":   true,
		"min_size": 1024,
		"levels":   map[string]interface{}{"gzip": 1},
	})
	downloadRoot := t.TempDir()
	if _, err := app.Route.Get("/unsafe-download", func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().DownloadSafe(downloadRoot, "../secret.txt", "")
	}); err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/unsafe-download", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	newTestHTTPHandler(t, app).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("安全下载拒绝响应应保持 403，实际为 %d，响应=%q", recorder.Code, recorder.Body.String())
	}
}

// TestDispatchMasksInternalErrorsOutsideDebug 验证生产模式下 dispatch 不会把内部容器错误明文返回给客户端。
func TestDispatchMasksInternalErrorsOutsideDebug(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.Container = framework.NewContainer()
	app.DebugMode = false

	handler := newTestHTTPHandler(t, app)
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/private", nil))
	resp := handler.dispatch(routeForDispatchTest(t, "MissingController@Show"), req)

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

	handler := newTestHTTPHandler(t, app)
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/private", nil))
	resp := handler.dispatch(routeForDispatchTest(t, "MissingController@Show"), req)

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

	handler := newTestHTTPHandler(t, app)
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/private", nil))
	resp := handler.dispatch(routeForDispatchTest(t, "BrokenController@Missing"), req)

	if resp.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("缺失方法时应返回 500，实际为 %d", resp.GetStatus())
	}
	if body := string(resp.GetBody()); body != "Internal Server Error" {
		t.Fatalf("生产模式下方法解析错误不应泄露细节，实际响应为 %q", body)
	}
}

// TestRequestLogPathKeepsControlCharactersEscaped 验证访问日志不会把 URL 中的编码换行解码成可伪造日志记录的控制字符。
func TestRequestLogPathKeepsControlCharactersEscaped(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://example.com/safe%0Aforged", nil)
	path := requestLogPath(request)
	if strings.ContainsAny(path, "\r\n") {
		t.Fatalf("访问日志路径不得包含换行控制字符: %q", path)
	}
	if !strings.Contains(strings.ToLower(path), "%0a") {
		t.Fatalf("访问日志路径应保留转义表示，实际为 %q", path)
	}
}

func routeForDispatchTest(t *testing.T, handler route.HandlerFunc) *route.Route {
	t.Helper()
	router := route.NewRouter()
	registered, err := router.Get("/private", handler)
	if err != nil {
		t.Fatalf("注册分发测试路由失败: %v", err)
	}
	return registered
}
