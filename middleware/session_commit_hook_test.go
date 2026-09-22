package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/session"
	"github.com/zhuhanxin0308/thinkgo/framework/session/driver"
)

type middlewareCommitHookWriter struct {
	header http.Header
	hook   func(http.Header) error
}

func (w *middlewareCommitHookWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *middlewareCommitHookWriter) Write(body []byte) (int, error) { return len(body), nil }
func (w *middlewareCommitHookWriter) WriteHeader(int)                {}

func (w *middlewareCommitHookWriter) BeforeCommit(hook func(http.Header) error) error {
	w.hook = hook
	return nil
}

func (w *middlewareCommitHookWriter) commit() error {
	if w.hook == nil {
		return errors.New("提交钩子未注册")
	}
	return w.hook(w.Header())
}

func TestSessionCommittedResponseDefersSaveUntilWriterBoundary(t *testing.T) {
	writer := &middlewareCommitHookWriter{}
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/committed", nil)
	req := context.MustNewRequest(raw, context.WithResponseWriter(writer))
	mw := &Session{Manager: newMiddlewareSessionManager(t, driver.NewMemory())}
	var requestSession *session.Session
	response := mw.Handle(req, func(req *context.Request) *context.Response {
		requestSession = req.GetData("_session").(*session.Session)
		if err := requestSession.Set("uid", 1001); err != nil {
			t.Fatalf("设置提交前 Session 失败: %v", err)
		}
		return context.NewCommittedResponse(http.StatusNoContent)
	})
	if response == nil || !response.Committed() || len(writer.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("尚未越过 writer 边界时不应提前写 Cookie: response=%#v header=%v", response, writer.Header())
	}
	if err := writer.commit(); err != nil {
		t.Fatalf("执行最终提交钩子失败: %v", err)
	}
	if len(writer.Header().Values("Set-Cookie")) != 1 {
		t.Fatalf("最终边界必须保存 Committed Response 的 Session: %v", writer.Header())
	}
	if err := requestSession.Set("late", true); !errors.Is(err, session.ErrSessionCommitted) {
		t.Fatalf("最终边界后的 Session 变更必须失败: %v", err)
	}
}

func TestSessionCommitHookPersistsLateStreamMutationAndDeduplicatesCookie(t *testing.T) {
	writer := &middlewareCommitHookWriter{}
	req := context.MustNewRequest(
		httptest.NewRequest(http.MethodGet, "http://example.com/stream", nil),
		context.WithResponseWriter(writer),
	)
	mw := &Session{Manager: newMiddlewareSessionManager(t, driver.NewMemory())}
	var requestSession *session.Session
	response := mw.Handle(req, func(req *context.Request) *context.Response {
		requestSession = req.GetData("_session").(*session.Session)
		if err := requestSession.Set("before-response", true); err != nil {
			t.Fatalf("设置响应构造期 Session 失败: %v", err)
		}
		return context.NewResponse().Content("stream")
	})
	for key, values := range response.Headers() {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
	if values := writer.Header().Values("Set-Cookie"); len(values) != 1 {
		t.Fatalf("响应构造期保存应先产生一个 Session Cookie: %#v", values)
	}
	if err := requestSession.Set("before-first-write", true); err != nil {
		t.Fatalf("首个流写入前应允许 Session 变更: %v", err)
	}
	if err := writer.commit(); err != nil {
		t.Fatalf("流式最终提交失败: %v", err)
	}
	if values := writer.Header().Values("Set-Cookie"); len(values) != 1 {
		t.Fatalf("多阶段保存后只应保留最终 Session Cookie: %#v", values)
	}

	headerWriter := &sessionHeaderWriter{}
	if headerWriter.Header() == nil {
		t.Fatal("Session 头 writer 必须惰性创建可用 Header")
	}
	if written, err := headerWriter.Write([]byte("ignored")); err != nil || written != len("ignored") {
		t.Fatalf("Session 头 writer 长度契约错误: written=%d err=%v", written, err)
	}
	headerWriter.WriteHeader(http.StatusCreated)
}

func TestSessionCommitHookMapsPersistenceConflict(t *testing.T) {
	writer := &middlewareCommitHookWriter{}
	req := context.MustNewRequest(
		httptest.NewRequest(http.MethodPost, "http://example.com/conflict", nil),
		context.WithResponseWriter(writer),
	)
	mw := &Session{Manager: newMiddlewareSessionManager(t, &failingSessionDriver{updateErr: session.ErrSessionRevoked})}
	var commitErr error
	response := mw.Handle(req, func(req *context.Request) *context.Response {
		requestSession := req.GetData("_session").(*session.Session)
		if err := requestSession.Set("uid", 1001); err != nil {
			t.Fatalf("设置冲突 Session 失败: %v", err)
		}
		commitErr = writer.commit()
		return context.NewCommittedResponse(http.StatusOK)
	})
	if response == nil || commitErr == nil || !errors.Is(commitErr, session.ErrSessionRevoked) {
		t.Fatalf("提交冲突必须保留原始错误链: response=%#v err=%v", response, commitErr)
	}
	provider, ok := commitErr.(interface{ ResponseCommitStatus() int })
	if !ok {
		t.Fatalf("Session 冲突错误必须提供响应状态: %T", commitErr)
	}
	if provider.ResponseCommitStatus() != http.StatusConflict {
		t.Fatalf("Session 冲突应映射为 409: provider=%T status=%d", commitErr, provider.ResponseCommitStatus())
	}
	if commitErr.Error() == "" {
		t.Fatal("Session 提交错误必须提供诊断文本")
	}
}
