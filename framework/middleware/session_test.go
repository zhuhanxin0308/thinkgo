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

// TestSessionMiddlewareKeepsNilResponseNil 验证下游返回 nil 时 Session 不应空指针崩溃，
// 也不应把 nil 响应伪装成成功响应，交由 HTTP 内核统一兜底处理。
func TestSessionMiddlewareKeepsNilResponseNil(t *testing.T) {
	manager := session.NewSession(
		map[string]interface{}{"name": "SID"},
		driver.NewMemory(),
		cookie.NewCookie(map[string]interface{}{"path": "/"}),
	)
	mw := &Session{Manager: manager}
	req := context.NewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil))

	resp := mw.Handle(req, func(req *context.Request) *context.Response {
		reqSession, ok := req.GetData("_session").(*session.Session)
		if !ok {
			t.Fatal("Session 中间件应把请求级会话写入上下文")
		}
		reqSession.Set("uid", 1001)
		return nil
	})

	if resp != nil {
		t.Fatalf("下游 nil 响应应保持为 nil，实际为 %#v", resp)
	}
}

// TestSessionMiddlewareFailsWhenSaveFails 验证会话落盘失败时不能继续返回业务成功响应。
func TestSessionMiddlewareFailsWhenSaveFails(t *testing.T) {
	manager := session.NewSession(
		map[string]interface{}{"name": "SID"},
		&failingSessionDriver{},
		cookie.NewCookie(map[string]interface{}{"path": "/"}),
	)
	mw := &Session{Manager: manager}
	req := context.NewRequest(httptest.NewRequest(http.MethodPost, "http://example.com/login", nil))

	resp := mw.Handle(req, func(req *context.Request) *context.Response {
		reqSession, ok := req.GetData("_session").(*session.Session)
		if !ok {
			t.Fatal("Session 中间件应把请求级会话写入上下文")
		}
		reqSession.Set("uid", 1001)
		return context.NewResponse().Content("ok")
	})

	if resp == nil {
		t.Fatal("保存失败应返回明确错误响应，而不是 nil")
	}
	if resp.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("保存失败应返回 500，实际为 %d，body=%q", resp.GetStatus(), string(resp.GetBody()))
	}
}

type failingSessionDriver struct{}

func (f *failingSessionDriver) Read(id string) (string, error) {
	return "", nil
}

func (f *failingSessionDriver) Write(id string, data string) error {
	return errors.New("write session failed")
}

func (f *failingSessionDriver) Delete(id string) error {
	return nil
}

func (f *failingSessionDriver) Clear() error {
	return nil
}
