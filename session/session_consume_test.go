package session

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestConsumeStringRejectsStaleAndWrongAttempts 验证错误尝试也在存储中原子销毁令牌。
func TestConsumeStringRejectsStaleAndWrongAttempts(t *testing.T) {
	manager, _, cookie := newPersistedStringSession(t)
	first := loadStringSession(t, manager, cookie)
	stale := loadStringSession(t, manager, cookie)
	accepted, err := first.ConsumeString("token", "wrong")
	if err != nil || accepted {
		t.Fatalf("错误令牌不应被接受: accepted=%t err=%v", accepted, err)
	}
	accepted, err = stale.ConsumeString("token", "secret")
	if err != nil || accepted {
		t.Fatalf("旧快照令牌不应再次被接受: accepted=%t err=%v", accepted, err)
	}
}

// TestConsumeStringKeepsTokenOnStorageError 验证存储故障时不会假装消费成功。
func TestConsumeStringKeepsTokenOnStorageError(t *testing.T) {
	manager, driver, cookie := newPersistedStringSession(t)
	request := loadStringSession(t, manager, cookie)
	storageError := errors.New("测试存储故障")
	driver.updateErr = storageError
	accepted, err := request.ConsumeString("token", "secret")
	if accepted || !errors.Is(err, storageError) || !request.Has("token") {
		t.Fatalf("存储故障后的请求状态错误: accepted=%t err=%v has=%t", accepted, err, request.Has("token"))
	}
	driver.updateErr = nil
	accepted, err = request.ConsumeString("token", "secret")
	if err != nil || !accepted || request.Has("token") {
		t.Fatalf("存储恢复后应能消费令牌: accepted=%t err=%v has=%t", accepted, err, request.Has("token"))
	}
}

func newPersistedStringSession(t *testing.T) (*Session, *countingDriver, *http.Cookie) {
	t.Helper()
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	initialWriter := httptest.NewRecorder()
	initial, err := manager.NewRequestSession(httptest.NewRequest(http.MethodPost, "http://example.com/form", nil), initialWriter)
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Set("token", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := initial.Save(); err != nil {
		t.Fatal(err)
	}
	cookies := initialWriter.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Session Cookie 数量错误: %d", len(cookies))
	}
	return manager, driver, cookies[0]
}

func loadStringSession(t *testing.T, manager *Session, cookie *http.Cookie) *Session {
	t.Helper()
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/form", nil)
	raw.AddCookie(cookie)
	request, err := manager.NewRequestSession(raw, httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	return request
}
