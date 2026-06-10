package session

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"thinkgo/framework/cookie"
	sessionDriver "thinkgo/framework/session/driver"
)

type countingDriver struct {
	readData      string
	writeCount    int
	deleteCount   int
	lastWriteID   string
	lastWriteData string
}

func (d *countingDriver) Read(id string) (string, error) {
	return d.readData, nil
}

func (d *countingDriver) Write(id string, data string) error {
	d.writeCount++
	d.lastWriteID = id
	d.lastWriteData = data
	return nil
}

func (d *countingDriver) Delete(id string) error {
	d.deleteCount++
	return nil
}

func (d *countingDriver) Clear() error {
	return nil
}

// newTestSessionManager 创建只依赖内存对象的 Session 管理器，便于验证框架行为。
func newTestSessionManager(driver Driver, sessionConfig map[string]interface{}, cookieConfig map[string]interface{}) *Session {
	return NewSession(sessionConfig, driver, cookie.NewCookie(cookieConfig))
}

// TestSaveSkipsUntouchedSession 验证未使用的 Session 不会产生无意义的写盘和 Set-Cookie。
func TestSaveSkipsUntouchedSession(t *testing.T) {
	driver := &countingDriver{}
	manager := newTestSessionManager(driver, map[string]interface{}{
		"name":   "PHPSESSID",
		"expire": 600,
	}, map[string]interface{}{})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	recorder := httptest.NewRecorder()

	reqSession := manager.NewRequestSession(req, recorder)
	reqSession.SetResponseWriter(recorder)
	if err := reqSession.Save(); err != nil {
		t.Fatalf("未使用 Session 时保存不应报错: %v", err)
	}

	if driver.writeCount != 0 {
		t.Fatalf("未使用的 Session 不应写入存储，实际写入 %d 次", driver.writeCount)
	}
	if len(recorder.Result().Cookies()) != 0 {
		t.Fatal("未使用的 Session 不应下发新的 Session Cookie")
	}
}

// TestDestroyThenSaveDoesNotRecreateSession 验证销毁后的 Session 不会在请求结束时被重新创建。
func TestDestroyThenSaveDoesNotRecreateSession(t *testing.T) {
	driver := &countingDriver{}
	manager := newTestSessionManager(driver, map[string]interface{}{
		"name":   "PHPSESSID",
		"expire": 600,
	}, map[string]interface{}{})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/logout", nil)
	recorder := httptest.NewRecorder()

	reqSession := manager.NewRequestSession(req, recorder)
	reqSession.Set("user_id", 1)
	reqSession.SetResponseWriter(recorder)
	reqSession.Destroy()
	if err := reqSession.Save(); err != nil {
		t.Fatalf("销毁 Session 后保存不应报错: %v", err)
	}

	if driver.deleteCount != 1 {
		t.Fatalf("销毁 Session 时应删除存储数据，实际删除 %d 次", driver.deleteCount)
	}
	if driver.writeCount != 0 {
		t.Fatalf("销毁后的 Session 不应再次写入存储，实际写入 %d 次", driver.writeCount)
	}
	if len(recorder.Result().Cookies()) != 1 {
		t.Fatalf("销毁 Session 后只应返回一个删除 Cookie，实际为 %d 个", len(recorder.Result().Cookies()))
	}
}

// TestSaveUsesSessionCookieOptions 验证 Session 自身配置会落到最终 Cookie 策略中。
func TestSaveUsesSessionCookieOptions(t *testing.T) {
	driver := &countingDriver{}
	manager := newTestSessionManager(driver, map[string]interface{}{
		"name":     "PHPSESSID",
		"expire":   600,
		"path":     "/admin",
		"domain":   "example.com",
		"secure":   true,
		"httponly": true,
		"samesite": "Strict",
	}, map[string]interface{}{
		"path":     "/",
		"samesite": "Lax",
	})

	req := httptest.NewRequest(http.MethodGet, "https://example.com/admin", nil)
	recorder := httptest.NewRecorder()

	reqSession := manager.NewRequestSession(req, recorder)
	reqSession.Set("role", "admin")
	reqSession.SetResponseWriter(recorder)
	if err := reqSession.Save(); err != nil {
		t.Fatalf("保存 Session Cookie 选项时不应报错: %v", err)
	}

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("应返回一个 Session Cookie，实际为 %d 个", len(cookies))
	}

	sessionCookie := cookies[0]
	if sessionCookie.Path != "/admin" {
		t.Fatalf("Session Cookie Path 不正确，期望 /admin，实际 %q", sessionCookie.Path)
	}
	if sessionCookie.Domain != "example.com" {
		t.Fatalf("Session Cookie Domain 不正确，期望 example.com，实际 %q", sessionCookie.Domain)
	}
	if !sessionCookie.Secure {
		t.Fatal("Session Cookie 应开启 Secure")
	}
	if !sessionCookie.HttpOnly {
		t.Fatal("Session Cookie 应开启 HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("Session Cookie SameSite 不正确，期望 Strict，实际 %v", sessionCookie.SameSite)
	}
}

// TestNewRequestSessionReplacesUnsafeSessionID 验证危险的外部 Session ID 不会继续参与后续存储。
func TestNewRequestSessionReplacesUnsafeSessionID(t *testing.T) {
	driver := &countingDriver{}
	manager := newTestSessionManager(driver, map[string]interface{}{
		"name":   "PHPSESSID",
		"expire": 600,
	}, map[string]interface{}{})

	traversalID := "../secret.txt"
	req := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	req.AddCookie(&http.Cookie{Name: "PHPSESSID", Value: traversalID})
	recorder := httptest.NewRecorder()

	reqSession := manager.NewRequestSession(req, recorder)
	reqSession.Set("user_id", 99)
	reqSession.SetResponseWriter(recorder)
	if err := reqSession.Save(); err != nil {
		t.Fatalf("替换危险 Session ID 后保存不应报错: %v", err)
	}

	if reqSession.id == traversalID {
		t.Fatal("危险的 Session ID 应被替换，而不是直接沿用客户端输入")
	}
	if strings.Contains(driver.lastWriteID, "..") || strings.Contains(driver.lastWriteID, "/") || strings.Contains(driver.lastWriteID, "\\") {
		t.Fatalf("落盘的 Session ID 仍然不安全: %q", driver.lastWriteID)
	}
}

// TestRequestSessionExpiresOnServer 验证即便客户端仍带着旧 Cookie，服务端也会拒绝已过期的 Session。
func TestRequestSessionExpiresOnServer(t *testing.T) {
	manager := newTestSessionManager(sessionDriver.NewMemory(), map[string]interface{}{
		"name":   "PHPSESSID",
		"expire": 1,
	}, map[string]interface{}{})

	firstReq := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	firstRecorder := httptest.NewRecorder()

	firstSession := manager.NewRequestSession(firstReq, firstRecorder)
	firstSession.Set("user_id", 7)
	firstSession.SetResponseWriter(firstRecorder)
	if err := firstSession.Save(); err != nil {
		t.Fatalf("首次保存 Session 不应报错: %v", err)
	}

	cookies := firstRecorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("首次保存后应下发一个 Session Cookie，实际为 %d 个", len(cookies))
	}

	time.Sleep(1200 * time.Millisecond)

	secondReq := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	secondReq.AddCookie(cookies[0])
	secondSession := manager.NewRequestSession(secondReq, httptest.NewRecorder())

	if secondSession.Get("user_id") != nil {
		t.Fatal("已过期的服务端 Session 不应再恢复旧数据")
	}
}
