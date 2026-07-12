package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"thinkgo/framework/context"
	"thinkgo/framework/cookie"
	"thinkgo/framework/session"
	"thinkgo/framework/session/driver"
)

// newMiddlewareSessionManager 创建严格校验后的中间件测试 Session 管理器。
func newMiddlewareSessionManager(t *testing.T, backend session.Driver) *session.Session {
	t.Helper()
	cookieFactory, err := cookie.NewCookie(map[string]interface{}{"path": "/"})
	if err != nil {
		t.Fatalf("创建 Cookie 工厂失败: %v", err)
	}
	manager, err := session.NewSession(map[string]interface{}{"name": "SID"}, backend, cookieFactory)
	if err != nil {
		t.Fatalf("创建 Session 管理器失败: %v", err)
	}
	return manager
}

// TestSessionMiddlewareKeepsNilResponseNil 验证下游 nil 响应不会触发 Cookie 写入或伪造成功响应。
func TestSessionMiddlewareKeepsNilResponseNil(t *testing.T) {
	mw := &Session{Manager: newMiddlewareSessionManager(t, driver.NewMemory())}
	req := context.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil))
	resp := mw.Handle(req, func(req *context.Request) *context.Response {
		reqSession, ok := req.GetData("_session").(*session.Session)
		if !ok {
			t.Fatal("Session 中间件应写入请求级会话")
		}
		if err := reqSession.Set("uid", 1001); err != nil {
			t.Fatalf("设置请求 Session 失败: %v", err)
		}
		return nil
	})
	if resp != nil {
		t.Fatalf("下游 nil 响应应保持 nil，实际为 %#v", resp)
	}
}

// TestSessionMiddlewareFailsWhenInitializationFails 验证读取存储失败时不会进入业务处理。
func TestSessionMiddlewareFailsWhenInitializationFails(t *testing.T) {
	backend := &failingSessionDriver{readErr: errors.New("read session failed")}
	mw := &Session{Manager: newMiddlewareSessionManager(t, backend)}
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	raw.AddCookie(&http.Cookie{Name: "SID", Value: "known-id"})
	req := context.MustNewRequest(raw)
	called := false
	resp := mw.Handle(req, func(*context.Request) *context.Response {
		called = true
		return context.NewResponse().Content("ok")
	})
	if called || resp == nil || resp.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("初始化失败应返回 500 且不调用下游: called=%t resp=%#v", called, resp)
	}
}

// TestSessionMiddlewareFailsWhenSaveFails 验证会话落盘失败不能返回业务成功响应。
func TestSessionMiddlewareFailsWhenSaveFails(t *testing.T) {
	backend := &failingSessionDriver{updateErr: errors.New("write session failed")}
	mw := &Session{Manager: newMiddlewareSessionManager(t, backend)}
	req := context.MustNewRequest(httptest.NewRequest(http.MethodPost, "http://example.com/login", nil))
	resp := mw.Handle(req, func(req *context.Request) *context.Response {
		reqSession := req.GetData("_session").(*session.Session)
		if err := reqSession.Set("uid", 1001); err != nil {
			t.Fatalf("设置请求 Session 失败: %v", err)
		}
		return context.NewResponse().Content("ok")
	})
	if resp == nil || resp.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("保存失败应返回 500，实际为 %#v", resp)
	}
}

// TestSessionMiddlewareMapsClientAndConflictErrors 验证 Cookie 歧义与撤销冲突使用可操作的 HTTP 状态。
func TestSessionMiddlewareMapsClientAndConflictErrors(t *testing.T) {
	manager := newMiddlewareSessionManager(t, driver.NewMemory())
	duplicate := httptest.NewRequest(http.MethodGet, "/", nil)
	duplicate.Header.Set("Cookie", "SID=first; SID=second")
	response := (&Session{Manager: manager}).Handle(
		context.MustNewRequest(duplicate),
		func(*context.Request) *context.Response {
			t.Fatal("重复 Session Cookie 不应进入下游")
			return nil
		},
	)
	if response.GetStatus() != http.StatusBadRequest {
		t.Fatalf("重复 Session Cookie 应返回 400，实际为 %d", response.GetStatus())
	}

	conflict := &Session{Manager: newMiddlewareSessionManager(t, &failingSessionDriver{updateErr: session.ErrSessionRevoked})}
	response = conflict.Handle(
		context.MustNewRequest(httptest.NewRequest(http.MethodPost, "/", nil)),
		func(req *context.Request) *context.Response {
			if err := req.GetData("_session").(*session.Session).Set("uid", 1); err != nil {
				t.Fatalf("设置冲突测试 Session 失败: %v", err)
			}
			return context.NewResponse().Content("ok")
		},
	)
	if response.GetStatus() != http.StatusConflict {
		t.Fatalf("撤销冲突应返回 409，实际为 %d", response.GetStatus())
	}
}

// TestSessionMiddlewareCommitsSetCookieToResponse 验证持久化后的 Cookie 真正进入框架响应，而不是写入头副本。
func TestSessionMiddlewareCommitsSetCookieToResponse(t *testing.T) {
	mw := &Session{Manager: newMiddlewareSessionManager(t, driver.NewMemory())}
	response := mw.Handle(
		context.MustNewRequest(httptest.NewRequest(http.MethodPost, "/login", nil)),
		func(req *context.Request) *context.Response {
			if err := req.GetData("_session").(*session.Session).Set("uid", 1); err != nil {
				t.Fatalf("设置 Session 失败: %v", err)
			}
			return context.NewResponse().Content("ok")
		},
	)
	if values := response.Headers().Values("Set-Cookie"); len(values) != 1 {
		t.Fatalf("Session Cookie 未提交到真实响应头: %#v", values)
	}
}

// TestSessionMiddlewareRejectsMissingManager 验证配置错误不会触发 nil 指针崩溃。
func TestSessionMiddlewareRejectsMissingManager(t *testing.T) {
	mw := &Session{}
	req := context.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil))
	called := false
	resp := mw.Handle(req, func(*context.Request) *context.Response {
		called = true
		return context.NewResponse()
	})
	if called || resp == nil || resp.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("缺少 Manager 应返回 500 且不调用下游: called=%t resp=%#v", called, resp)
	}
}

// TestResponseAdapterImplementsWriterContract 验证适配器暂存头并通过受校验 API 提交到真实响应。
func TestResponseAdapterImplementsWriterContract(t *testing.T) {
	response := context.NewResponse()
	adapter := &ResponseAdapter{response: response}
	adapter.Header().Set("X-Test", "value")
	if response.Headers().Get("X-Test") != "" {
		t.Fatal("ResponseAdapter 提交前不应绕过 context.Response 直接改头")
	}
	if err := adapter.Commit(); err != nil {
		t.Fatalf("提交暂存响应头失败: %v", err)
	}
	if response.Headers().Get("X-Test") != "value" {
		t.Fatal("ResponseAdapter 未把暂存头提交到真实响应")
	}
	if written, err := adapter.Write([]byte("data")); err != nil || written != 4 {
		t.Fatalf("ResponseAdapter.Write 长度契约错误: written=%d err=%v", written, err)
	}
	adapter.WriteHeader(http.StatusNoContent)
	if header := (*ResponseAdapter)(nil).Header(); header == nil {
		t.Fatal("nil ResponseAdapter.Header 应返回可用空 Header")
	}
}

type failingSessionDriver struct {
	readErr   error
	updateErr error
}

func (f *failingSessionDriver) Read(string) (string, bool, error) {
	return "", false, f.readErr
}

func (f *failingSessionDriver) Write(string, string) error { return f.updateErr }

func (f *failingSessionDriver) Delete(string) error { return nil }

func (f *failingSessionDriver) Clear() error { return nil }

func (f *failingSessionDriver) Update(_ string, update func(string, bool) (string, bool, error)) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	_, _, err := update("", false)
	return err
}
