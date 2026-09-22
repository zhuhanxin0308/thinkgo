package context

import (
	"net/http"
	"net/http/httptest"
	"testing"

	frameworkcookie "github.com/zhuhanxin0308/thinkgo/framework/cookie"
	frameworksession "github.com/zhuhanxin0308/thinkgo/framework/session"
	sessiondriver "github.com/zhuhanxin0308/thinkgo/framework/session/driver"
)

// TestResponseThinkPHPCookieAndSessionServicesAPI 验证 GetCookie 返回隔离快照，
// SetSession 绑定的请求会话会在响应头提交前完成保存。
func TestResponseThinkPHPCookieAndSessionServicesAPI(t *testing.T) {
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	recorder := httptest.NewRecorder()
	cookieFactory, err := frameworkcookie.NewCookie(map[string]interface{}{})
	if err != nil {
		t.Fatalf("创建 Cookie 工厂失败: %v", err)
	}
	manager, err := frameworksession.NewSession(map[string]interface{}{"name": "THINKGOSESSID"}, sessiondriver.NewMemory(), cookieFactory)
	if err != nil {
		t.Fatalf("创建 Session 管理器失败: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	requestSession, err := manager.NewRequestSession(raw, nil)
	if err != nil {
		t.Fatalf("创建请求 Session 失败: %v", err)
	}
	if err = requestSession.Set("uid", 1001); err != nil {
		t.Fatalf("设置 Session 数据失败: %v", err)
	}

	response := NewResponse().Content("ok").Cookie("theme", "dark")
	cookies := response.GetCookie()
	if len(cookies) != 1 || cookies[0].Name != "theme" || cookies[0].Value != "dark" {
		t.Fatalf("GetCookie 返回错误: %#v", cookies)
	}
	cookies[0].Value = "changed"
	if response.GetCookie()[0].Value != "dark" {
		t.Fatal("GetCookie 不得暴露响应内部 Cookie 状态")
	}
	if response.SetSession(requestSession) != response {
		t.Fatal("SetSession 必须支持链式调用")
	}
	if err = response.Send(recorder); err != nil {
		t.Fatalf("发送带 Session 的响应失败: %v", err)
	}

	written := recorder.Result().Cookies()
	names := make(map[string]bool, len(written))
	for _, current := range written {
		names[current.Name] = true
	}
	if !names["theme"] || !names["THINKGOSESSID"] || recorder.Body.String() != "ok" {
		t.Fatalf("业务 Cookie、Session Cookie 或响应体未完整提交: cookies=%#v body=%q", written, recorder.Body.String())
	}
}
