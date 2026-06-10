package session

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNewRequestSessionReplacesUnknownButSafeSessionID 验证客户端自带但后端并不存在的合法 Session ID
// 不会被直接复用，避免攻击者预设固定会话标识。
func TestNewRequestSessionReplacesUnknownButSafeSessionID(t *testing.T) {
	driver := &countingDriver{}
	manager := newTestSessionManager(driver, map[string]interface{}{
		"name":   "PHPSESSID",
		"expire": 600,
	}, map[string]interface{}{})

	fixedID := "fixed-session-id-from-client"
	req := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	req.AddCookie(&http.Cookie{Name: "PHPSESSID", Value: fixedID})
	recorder := httptest.NewRecorder()

	reqSession := manager.NewRequestSession(req, recorder)
	reqSession.Set("user_id", 100)
	reqSession.SetResponseWriter(recorder)
	if err := reqSession.Save(); err != nil {
		t.Fatalf("固定会话防护场景下保存不应报错: %v", err)
	}

	if reqSession.id == fixedID {
		t.Fatal("未知但格式合法的 Session ID 不应被直接复用")
	}
	if driver.lastWriteID == fixedID {
		t.Fatal("落盘时不应继续使用客户端预设的 Session ID")
	}
	if driver.lastWriteID == "" {
		t.Fatal("写入存储时应使用新生成的 Session ID")
	}
}
