package http

import (
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	frameworkContext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/cookie"
	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
	frameworkSession "github.com/zhuhanxin0308/thinkgo/framework/session"
	sessionDriver "github.com/zhuhanxin0308/thinkgo/framework/session/driver"
)

func newHTTPCommitSessionManager(t *testing.T, backend frameworkSession.Driver) *frameworkSession.Session {
	t.Helper()
	cookieFactory, err := cookie.NewCookie(map[string]interface{}{"path": "/"})
	if err != nil {
		t.Fatalf("创建提交事务 Cookie 工厂失败: %v", err)
	}
	manager, err := frameworkSession.NewSession(map[string]interface{}{"name": "COMMITSESSID"}, backend, cookieFactory)
	if err != nil {
		t.Fatalf("创建提交事务 Session 管理器失败: %v", err)
	}
	return manager
}

func installSessionMutationPipeline(t *testing.T, handler *Http, manager *frameworkSession.Session) {
	t.Helper()
	handler.middleware.Pipe((&middleware.Session{Manager: manager}).Handle)
	handler.middleware.Pipe(func(req *frameworkContext.Request, next func(*frameworkContext.Request) *frameworkContext.Response) *frameworkContext.Response {
		requestSession, ok := req.GetData("_session").(*frameworkSession.Session)
		if !ok {
			t.Fatal("Session 中间件未注入请求会话")
		}
		if err := requestSession.Set("uid", 1001); err != nil {
			t.Fatalf("设置请求 Session 失败: %v", err)
		}
		return next(req)
	})
}

func TestStandardHandlerSessionCookieCommitsBeforeBusinessResponse(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	router := mustHTTPRoute(t, app)
	if _, err := router.Any("/commit-session", stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writer.WriteHeader(stdhttp.StatusCreated)
		_, _ = writer.Write([]byte("created"))
	})); err != nil {
		t.Fatalf("注册标准 Handler 失败: %v", err)
	}
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建 HTTP 内核失败: %v", err)
	}
	installSessionMutationPipeline(t, handler, newHTTPCommitSessionManager(t, sessionDriver.NewMemory()))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://127.0.0.1/commit-session", nil))
	if recorder.Code != stdhttp.StatusCreated || recorder.Body.String() != "created" {
		t.Fatalf("业务响应错误: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if values := recorder.Header().Values("Set-Cookie"); len(values) != 1 || !strings.Contains(values[0], "COMMITSESSID=") {
		t.Fatalf("Session Cookie 必须在标准 Handler 最终提交前写出: %#v", values)
	}
}

func TestServeHTTPStandardHandlerSeesOnlyRealWriterCapabilities(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	router := mustHTTPRoute(t, app)
	var hasFlusher bool
	var hasHijacker bool
	var hasPusher bool
	var flushErr error
	if _, err := router.Get("/capabilities", stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
		_, hasFlusher = writer.(stdhttp.Flusher)
		_, hasHijacker = writer.(stdhttp.Hijacker)
		_, hasPusher = writer.(stdhttp.Pusher)
		flushErr = stdhttp.NewResponseController(writer).Flush()
		writer.WriteHeader(stdhttp.StatusNoContent)
	})); err != nil {
		t.Fatalf("注册能力探针 Handler 失败: %v", err)
	}
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建 HTTP 内核失败: %v", err)
	}
	recorder := &responseProtocolRecorder{}
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://127.0.0.1/capabilities", nil))
	if hasFlusher || hasHijacker || hasPusher {
		t.Fatalf("标准 Handler 不得看到底层不存在的接口: flush=%t hijack=%t push=%t", hasFlusher, hasHijacker, hasPusher)
	}
	if !errors.Is(flushErr, stdhttp.ErrNotSupported) {
		t.Fatalf("ResponseController 不得命中旧包装器的伪能力: %v", flushErr)
	}
	if len(recorder.statuses) != 1 || recorder.statuses[0] != stdhttp.StatusNoContent {
		t.Fatalf("能力探针响应错误: %#v", recorder.statuses)
	}
}

func TestStandardHandlerEmptyCommittedResponseStillSavesSession(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	router := mustHTTPRoute(t, app)
	if _, err := router.Get("/commit-empty", stdhttp.HandlerFunc(func(stdhttp.ResponseWriter, *stdhttp.Request) {})); err != nil {
		t.Fatalf("注册空标准 Handler 失败: %v", err)
	}
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建 HTTP 内核失败: %v", err)
	}
	installSessionMutationPipeline(t, handler, newHTTPCommitSessionManager(t, sessionDriver.NewMemory()))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://127.0.0.1/commit-empty", nil))
	if recorder.Code != stdhttp.StatusOK || recorder.Body.Len() != 0 {
		t.Fatalf("空标准 Handler 响应错误: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if len(recorder.Header().Values("Set-Cookie")) != 1 {
		t.Fatalf("Committed Response 未主动写头时仍必须保存 Session: %v", recorder.Header())
	}
}

func TestStandardHandlerSessionSaveFailureReplacesSuccess(t *testing.T) {
	wantErr := errors.New("session persistence unavailable")
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	router := mustHTTPRoute(t, app)
	if _, err := router.Get("/commit-failure", stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writer.Header().Set("X-Business", "must-not-leak")
		writer.WriteHeader(stdhttp.StatusCreated)
		_, _ = writer.Write([]byte("business-success"))
	})); err != nil {
		t.Fatalf("注册失败场景 Handler 失败: %v", err)
	}
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建 HTTP 内核失败: %v", err)
	}
	installSessionMutationPipeline(t, handler, newHTTPCommitSessionManager(t, &httpCommitFailingSessionDriver{updateErr: wantErr}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://127.0.0.1/commit-failure", nil))
	if recorder.Code != stdhttp.StatusInternalServerError || recorder.Body.String() != stdhttp.StatusText(stdhttp.StatusInternalServerError) {
		t.Fatalf("Session 保存失败必须替换业务成功: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("X-Business") != "" {
		t.Fatalf("提交失败后不得泄漏业务响应头: %v", recorder.Header())
	}
}

func TestStandardHandlerPanicBeforeAndAfterFinalCommit(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	router := mustHTTPRoute(t, app)
	if _, err := router.Get("/panic-before", stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writer.Header().Set("X-Stale-Business", "stale")
		panic("before commit")
	})); err != nil {
		t.Fatalf("注册提交前 panic Handler 失败: %v", err)
	}
	if _, err := router.Get("/panic-after", stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writer.WriteHeader(stdhttp.StatusAccepted)
		_, _ = writer.Write([]byte("partial"))
		panic("after commit")
	})); err != nil {
		t.Fatalf("注册提交后 panic Handler 失败: %v", err)
	}
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建 HTTP 内核失败: %v", err)
	}

	before := httptest.NewRecorder()
	handler.ServeHTTP(before, httptest.NewRequest(stdhttp.MethodGet, "http://127.0.0.1/panic-before", nil))
	if before.Code != stdhttp.StatusInternalServerError || before.Header().Get("X-Stale-Business") != "" {
		t.Fatalf("提交前 panic 应安全改写且清除业务头: status=%d header=%v", before.Code, before.Header())
	}

	after := httptest.NewRecorder()
	func() {
		defer func() {
			if recovered := recover(); recovered != stdhttp.ErrAbortHandler {
				t.Fatalf("提交后的异常必须通知宿主中断传输: %v", recovered)
			}
		}()
		handler.ServeHTTP(after, httptest.NewRequest(stdhttp.MethodGet, "http://127.0.0.1/panic-after", nil))
	}()
	if after.Code != stdhttp.StatusAccepted || after.Body.String() != "partial" {
		t.Fatalf("提交后 panic 不得伪造第二份响应: status=%d body=%q", after.Code, after.Body.String())
	}
}

func TestStandardHandlerHeadWriteCommitsSessionWithoutEntity(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	router := mustHTTPRoute(t, app)
	if _, err := router.Any("/head-session", stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writer.WriteHeader(stdhttp.StatusAccepted)
		_, _ = writer.Write([]byte("must-not-be-sent"))
	})); err != nil {
		t.Fatalf("注册 HEAD Handler 失败: %v", err)
	}
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建 HTTP 内核失败: %v", err)
	}
	installSessionMutationPipeline(t, handler, newHTTPCommitSessionManager(t, sessionDriver.NewMemory()))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodHead, "http://127.0.0.1/head-session", nil))
	if recorder.Code != stdhttp.StatusAccepted || recorder.Body.Len() != 0 {
		t.Fatalf("HEAD 状态或实体错误: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if len(recorder.Header().Values("Set-Cookie")) != 1 {
		t.Fatalf("HEAD 最终提交前应保存 Session Cookie: %v", recorder.Header())
	}
}

func TestFrameworkStreamSealsSessionAtFirstWrite(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	router := mustHTTPRoute(t, app)
	var beforeWriteErr error
	var afterWriteErr error
	if _, err := router.Get("/stream-session", func(req *frameworkContext.Request) *frameworkContext.Response {
		requestSession := req.GetData("_session").(*frameworkSession.Session)
		return frameworkContext.NewResponse().Stream(func(writer io.Writer) error {
			beforeWriteErr = requestSession.Set("stream-before", true)
			if _, err := writer.Write([]byte("stream")); err != nil {
				return err
			}
			afterWriteErr = requestSession.Set("stream-after", true)
			return nil
		})
	}); err != nil {
		t.Fatalf("注册流式 Session 路由失败: %v", err)
	}
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建 HTTP 内核失败: %v", err)
	}
	handler.middleware.Pipe((&middleware.Session{Manager: newHTTPCommitSessionManager(t, sessionDriver.NewMemory())}).Handle)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://127.0.0.1/stream-session", nil))
	if recorder.Code != stdhttp.StatusOK || recorder.Body.String() != "stream" {
		t.Fatalf("流式 Session 响应错误: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if beforeWriteErr != nil || !errors.Is(afterWriteErr, frameworkSession.ErrSessionCommitted) {
		t.Fatalf("流式提交边界错误: before=%v after=%v", beforeWriteErr, afterWriteErr)
	}
	if len(recorder.Header().Values("Set-Cookie")) != 1 {
		t.Fatalf("首个流写入前的 mutation 必须落盘并写 Cookie: %v", recorder.Header())
	}
}

func TestTerminatorCannotMutateCommittedSession(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	router := mustHTTPRoute(t, app)
	if _, err := router.Get("/terminator-session", func(req *frameworkContext.Request) *frameworkContext.Response {
		requestSession := req.GetData("_session").(*frameworkSession.Session)
		if err := requestSession.Set("before-terminator", true); err != nil {
			t.Fatalf("设置终结器前 Session 失败: %v", err)
		}
		return frameworkContext.NewResponse().Content("ok")
	}); err != nil {
		t.Fatalf("注册终结器 Session 路由失败: %v", err)
	}
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建 HTTP 内核失败: %v", err)
	}
	handler.middleware.Pipe((&middleware.Session{Manager: newHTTPCommitSessionManager(t, sessionDriver.NewMemory())}).Handle)
	var terminatorErr error
	handler.middleware.PipeLifecycle(
		func(req *frameworkContext.Request, next func(*frameworkContext.Request) *frameworkContext.Response) *frameworkContext.Response {
			return next(req)
		},
		func(req *frameworkContext.Request, _ *frameworkContext.Response) {
			terminatorErr = req.GetData("_session").(*frameworkSession.Session).Set("too-late", true)
		},
	)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://127.0.0.1/terminator-session", nil))
	if recorder.Code != stdhttp.StatusOK || recorder.Body.String() != "ok" {
		t.Fatalf("终结器 Session 响应错误: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if !errors.Is(terminatorErr, frameworkSession.ErrSessionCommitted) {
		t.Fatalf("终结器不得静默修改已提交 Session: %v", terminatorErr)
	}
}

type httpCommitFailingSessionDriver struct {
	updateErr error
}

func (d *httpCommitFailingSessionDriver) Read(string) (string, bool, error) { return "", false, nil }
func (d *httpCommitFailingSessionDriver) Write(string, string) error        { return d.updateErr }
func (d *httpCommitFailingSessionDriver) Delete(string) error               { return nil }
func (d *httpCommitFailingSessionDriver) Clear() error                      { return nil }
func (d *httpCommitFailingSessionDriver) Update(_ string, update func(string, bool) (string, bool, error)) error {
	if d.updateErr != nil {
		return d.updateErr
	}
	_, _, err := update("", false)
	return err
}
