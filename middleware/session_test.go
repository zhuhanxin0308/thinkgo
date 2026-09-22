package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/cookie"
	"github.com/zhuhanxin0308/thinkgo/framework/session"
	"github.com/zhuhanxin0308/thinkgo/framework/session/driver"
)

const middlewareKnownSessionID = "0123456789abcdef0123456789abcdef"

// newMiddlewareSessionManager 创建严格校验后的中间件测试 Session 管理器。
func newMiddlewareSessionManager(t *testing.T, backend session.Driver) *session.Session {
	return newConfiguredMiddlewareSessionManager(t, backend, map[string]interface{}{"name": "SID"})
}

// newConfiguredMiddlewareSessionManager 使用指定的 ThinkPHP Session 配置创建管理器。
func newConfiguredMiddlewareSessionManager(t *testing.T, backend session.Driver, configuration map[string]interface{}) *session.Session {
	t.Helper()
	cookieFactory, err := cookie.NewCookie(map[string]interface{}{"path": "/"})
	if err != nil {
		t.Fatalf("创建 Cookie 工厂失败: %v", err)
	}
	manager, err := session.NewSession(configuration, backend, cookieFactory)
	if err != nil {
		t.Fatalf("创建 Session 管理器失败: %v", err)
	}
	return manager
}

// TestSessionMiddlewareUsesVarSessionIDBeforeCookie 验证跨域上传参数与
// ThinkPHP SessionInit 一致优先于 Cookie 恢复已有会话。
func TestSessionMiddlewareUsesVarSessionIDBeforeCookie(t *testing.T) {
	manager := newConfiguredMiddlewareSessionManager(t, driver.NewMemory(), map[string]interface{}{
		"name": "SID", "var_session_id": "upload_sid",
	})
	middleware := &Session{Manager: manager}
	firstResponse := middleware.Handle(
		context.MustNewRequest(httptest.NewRequest(http.MethodPost, "http://example.com/login", nil)),
		func(request *context.Request) *context.Response {
			if err := request.GetData("_session").(*session.Session).Set("uid", "1001"); err != nil {
				t.Fatalf("写入首次 Session 失败: %v", err)
			}
			return context.NewResponse().Content("ok")
		},
	)
	cookies := firstResponse.Headers().Values("Set-Cookie")
	if len(cookies) != 1 {
		t.Fatalf("首次响应应写入一个 Session Cookie: %#v", cookies)
	}
	parsedCookie, err := http.ParseSetCookie(cookies[0])
	if err != nil {
		t.Fatalf("解析首次 Session Cookie 失败: %v", err)
	}

	body := strings.NewReader("upload_sid=" + parsedCookie.Value)
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/upload", body)
	raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	secondResponse := middleware.Handle(context.MustNewRequest(raw), func(request *context.Request) *context.Response {
		uid, exists := request.GetData("_session").(*session.Session).Get("uid")
		if !exists || uid != "1001" {
			t.Fatalf("var_session_id 未恢复已有会话: uid=%#v exists=%t", uid, exists)
		}
		return context.NewResponse().Content("uploaded")
	})
	if secondResponse == nil || string(secondResponse.GetBody()) != "uploaded" {
		t.Fatalf("跨域上传请求未继续执行: %#v", secondResponse)
	}
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
	raw.AddCookie(&http.Cookie{Name: "SID", Value: middlewareKnownSessionID})
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

// TestSessionMiddlewarePropagatesTrustedSecureDecision 验证 Session 中间件只使用 Request 的可信协议结论。
func TestSessionMiddlewarePropagatesTrustedSecureDecision(t *testing.T) {
	manager := newMiddlewareSessionManager(t, driver.NewMemory())
	spoofed := httptest.NewRequest(http.MethodPost, "http://example.com/login", nil)
	spoofed.RemoteAddr = "198.51.100.8:4321"
	spoofed.Header.Set("X-Forwarded-Proto", "https")
	unsafeRequest := context.MustNewRequest(spoofed)
	unsafeResponse := (&Session{Manager: manager}).Handle(unsafeRequest, func(req *context.Request) *context.Response {
		if err := req.GetData("_session").(*session.Session).Set("uid", 1); err != nil {
			t.Fatalf("设置非安全 Session 失败: %v", err)
		}
		return context.NewResponse().Content("ok")
	})
	if unsafeResponse == nil || len(unsafeResponse.Headers().Values("Set-Cookie")) != 1 {
		t.Fatalf("非安全 Session 响应 Cookie 缺失: %#v", unsafeResponse)
	}
	if cookieValue := unsafeResponse.Headers().Get("Set-Cookie"); strings.Contains(cookieValue, "; Secure") {
		t.Fatalf("非可信请求头不应被 Session 中间件提升为 Secure: %s", cookieValue)
	}

	trustedRaw := httptest.NewRequest(http.MethodPost, "http://example.com/login", nil)
	trustedRaw.RemoteAddr = "127.0.0.1:4321"
	trustedRaw.Header.Set("X-Forwarded-Proto", "https")
	trustedRequest := context.MustNewRequest(trustedRaw, context.WithTrustedProxies([]string{"127.0.0.1/32"}))
	trustedResponse := (&Session{Manager: manager}).Handle(trustedRequest, func(req *context.Request) *context.Response {
		if err := req.GetData("_session").(*session.Session).Set("uid", 2); err != nil {
			t.Fatalf("设置可信 Session 失败: %v", err)
		}
		return context.NewResponse().Content("ok")
	})
	if trustedResponse == nil || !strings.Contains(trustedResponse.Headers().Get("Set-Cookie"), "; Secure") {
		t.Fatalf("可信代理 HTTPS 请求应写入 Secure Session Cookie: %#v", trustedResponse)
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
