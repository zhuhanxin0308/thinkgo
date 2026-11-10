package session

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
)

// TestSessionReportsDisabledCookie 验证持久化后 Cookie 禁发错误不会被吞掉或清除待发送状态。
func TestSessionReportsDisabledCookie(t *testing.T) {
	manager := newTestSessionManager(t, newCountingDriver(), nil, map[string]interface{}{"setcookie": false})
	recorder := httptest.NewRecorder()
	current, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "http://example.com", nil), recorder)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Set("user", "synthetic-user"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := current.Save(); !errors.Is(err, ErrSessionCookie) || !errors.Is(err, cookie.ErrCookieDisabled) {
			t.Fatalf("Save 未报告 Cookie 发送失败: %v", err)
		}
		if len(recorder.Header().Values("Set-Cookie")) != 0 {
			t.Fatal("Session 绕过禁发配置")
		}
	}
	if err := current.Destroy(); err != nil {
		t.Fatal(err)
	}
	if err := current.Save(); !errors.Is(err, cookie.ErrCookieDisabled) {
		t.Fatalf("删除 Session Cookie 未报告禁发错误: %v", err)
	}
	if len(recorder.Header().Values("Set-Cookie")) != 0 {
		t.Fatal("Session 删除路径绕过禁发配置")
	}
}
