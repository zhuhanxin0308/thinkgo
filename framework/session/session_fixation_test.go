package session

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// persistedSessionFixture 创建已持久化会话及其客户端 Cookie，供重放与并发请求测试复用。
func persistedSessionFixture(t *testing.T, manager *Session) (*http.Cookie, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	reqSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/", nil), recorder)
	if err != nil {
		t.Fatalf("初始化基准 Session 失败: %v", err)
	}
	if err = reqSession.Set("uid", 100); err != nil {
		t.Fatalf("设置基准 Session 失败: %v", err)
	}
	if err = reqSession.Save(); err != nil {
		t.Fatalf("保存基准 Session 失败: %v", err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("基准 Session 应返回一个 Cookie，实际为 %d", len(cookies))
	}
	return cookies[0], reqSession.id
}

// requestFromSessionCookie 创建携带同一 Session Cookie 的独立请求。
func requestFromSessionCookie(cookieValue *http.Cookie) *http.Request {
	raw := httptest.NewRequest(http.MethodPost, "/", nil)
	copyCookie := *cookieValue
	raw.AddCookie(&copyCookie)
	return raw
}

// TestRegenerateRevokesOldIDAgainstStaleRequests 验证轮换后并发旧请求无法重建旧登录态。
func TestRegenerateRevokesOldIDAgainstStaleRequests(t *testing.T) {
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID", "expire": 600}, nil)
	cookieValue, oldID := persistedSessionFixture(t, manager)

	freshRecorder := httptest.NewRecorder()
	fresh, err := manager.NewRequestSession(requestFromSessionCookie(cookieValue), freshRecorder)
	if err != nil {
		t.Fatalf("初始化轮换请求失败: %v", err)
	}
	stale, err := manager.NewRequestSession(requestFromSessionCookie(cookieValue), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("初始化并发旧请求失败: %v", err)
	}
	if err = fresh.Set("role", "admin"); err != nil {
		t.Fatalf("设置提权数据失败: %v", err)
	}
	if err = fresh.Regenerate(); err != nil {
		t.Fatalf("轮换 Session ID 失败: %v", err)
	}
	if fresh.id == oldID {
		t.Fatal("Regenerate 必须生成新的 Session ID")
	}
	if err = fresh.Save(); err != nil {
		t.Fatalf("保存轮换后的 Session 失败: %v", err)
	}
	if err = stale.Set("stale", true); err != nil {
		t.Fatalf("设置并发旧请求数据失败: %v", err)
	}
	if err = stale.Save(); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("旧请求保存应返回 ErrSessionRevoked，实际为 %v", err)
	}

	raw := requestFromSessionCookie(cookieValue)
	replayed, err := manager.NewRequestSession(raw, httptest.NewRecorder())
	if err != nil {
		t.Fatalf("重放撤销 ID 应降级为匿名新会话: %v", err)
	}
	if replayed.id == oldID {
		t.Fatal("被撤销的 Session ID 不得再次复用")
	}
	if _, found := replayed.Get("uid"); found {
		t.Fatal("被撤销 ID 不得恢复旧数据")
	}
}

// TestDestroyRevokesOldIDAndDeletesClientCookie 验证退出登录同时撤销服务端 ID 与客户端 Cookie。
func TestDestroyRevokesOldIDAndDeletesClientCookie(t *testing.T) {
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID", "expire": 600}, nil)
	cookieValue, oldID := persistedSessionFixture(t, manager)
	recorder := httptest.NewRecorder()
	reqSession, err := manager.NewRequestSession(requestFromSessionCookie(cookieValue), recorder)
	if err != nil {
		t.Fatalf("初始化销毁请求失败: %v", err)
	}
	if err = reqSession.Destroy(); err != nil {
		t.Fatalf("销毁 Session 失败: %v", err)
	}
	if err = reqSession.Save(); err != nil {
		t.Fatalf("写入 Session 删除 Cookie 失败: %v", err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge != -1 {
		t.Fatalf("销毁后应下发一个删除 Cookie，实际为 %#v", cookies)
	}
	stored, found := driver.snapshot(oldID)
	if !found {
		t.Fatal("销毁后必须保留限时撤销墓碑，阻断并发旧请求重建")
	}
	envelope, err := decodeSessionEnvelope(stored, manager.config.MaxDataBytes)
	if err != nil || !envelope.Revoked || len(envelope.Data) != 0 {
		t.Fatalf("撤销墓碑内容错误: envelope=%#v err=%v", envelope, err)
	}
}

// TestDuplicateSessionCookiesAreRejected 验证同名 Cookie 歧义不会被任意选择。
func TestDuplicateSessionCookiesAreRejected(t *testing.T) {
	manager := newTestSessionManager(t, newCountingDriver(), map[string]interface{}{"name": "SID"}, nil)
	raw := httptest.NewRequest(http.MethodGet, "/", nil)
	raw.Header.Set("Cookie", "SID=first; SID=second")
	if _, err := manager.NewRequestSession(raw, httptest.NewRecorder()); !errors.Is(err, ErrSessionCookie) {
		t.Fatalf("重复 Session Cookie 应返回 ErrSessionCookie，实际为 %v", err)
	}
}
