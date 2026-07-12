package session

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type sessionLogRecord struct {
	msg string
	ctx map[string]interface{}
}

type sessionTestLogger struct {
	mu         sync.Mutex
	errorCalls []sessionLogRecord
}

func (l *sessionTestLogger) ErrorCtx(msg string, ctx map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errorCalls = append(l.errorCalls, sessionLogRecord{msg: msg, ctx: ctx})
}

func (l *sessionTestLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.errorCalls)
}

// TestRequestInitializationReturnsDriverReadError 验证后端读取故障不会伪装成新会话。
func TestRequestInitializationReturnsDriverReadError(t *testing.T) {
	backendErr := errors.New("backend unavailable")
	driver := newCountingDriver()
	driver.readErr = backendErr
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	logger := &sessionTestLogger{}
	manager.SetLogger(logger)
	raw := httptest.NewRequest(http.MethodGet, "/", nil)
	raw.AddCookie(&http.Cookie{Name: "SID", Value: "known-id"})

	if _, err := manager.NewRequestSession(raw, httptest.NewRecorder()); !errors.Is(err, backendErr) {
		t.Fatalf("读取错误应原样传播，实际为 %v", err)
	}
	if logger.count() != 1 {
		t.Fatalf("读取错误应记录一次，实际为 %d", logger.count())
	}
}

// TestSaveReturnsAtomicUpdateErrorAndRetainsDirtyState 验证原子持久化失败可重试且不会下发无效 Cookie。
func TestSaveReturnsAtomicUpdateErrorAndRetainsDirtyState(t *testing.T) {
	backendErr := errors.New("disk full")
	driver := newCountingDriver()
	driver.updateErr = backendErr
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	logger := &sessionTestLogger{}
	manager.SetLogger(logger)
	recorder := httptest.NewRecorder()
	reqSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/", nil), recorder)
	if err != nil {
		t.Fatalf("初始化请求 Session 失败: %v", err)
	}
	if err = reqSession.Set("uid", 1); err != nil {
		t.Fatalf("设置 Session 失败: %v", err)
	}
	if err = reqSession.Save(); !errors.Is(err, backendErr) {
		t.Fatalf("持久化错误应原样传播，实际为 %v", err)
	}
	if len(recorder.Result().Cookies()) != 0 || !reqSession.dirty {
		t.Fatalf("失败保存不得下发 Cookie 或清除 dirty: cookies=%d dirty=%t", len(recorder.Result().Cookies()), reqSession.dirty)
	}
	if logger.count() != 1 {
		t.Fatalf("持久化错误应记录一次，实际为 %d", logger.count())
	}
}

// TestSaveRequiresResponseWriterBeforeCookieMutation 验证数据落盘后 Cookie 写入失败仍保持可重试状态。
func TestSaveRequiresResponseWriterBeforeCookieMutation(t *testing.T) {
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	reqSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/", nil), nil)
	if err != nil {
		t.Fatalf("初始化请求 Session 失败: %v", err)
	}
	if err = reqSession.Set("uid", 1); err != nil {
		t.Fatalf("设置 Session 失败: %v", err)
	}
	if err = reqSession.Save(); !errors.Is(err, ErrSessionCookie) {
		t.Fatalf("缺少 writer 应返回 ErrSessionCookie，实际为 %v", err)
	}
	if _, found := driver.snapshot(reqSession.id); !found || !reqSession.cookieDirty {
		t.Fatalf("数据应已持久化且 Cookie 保持待写状态: found=%t cookieDirty=%t", found, reqSession.cookieDirty)
	}
	recorder := httptest.NewRecorder()
	if err = reqSession.SetResponseWriter(recorder); err != nil {
		t.Fatalf("绑定 writer 失败: %v", err)
	}
	if err = reqSession.Save(); err != nil {
		t.Fatalf("重试 Cookie 写入失败: %v", err)
	}
	if len(recorder.Result().Cookies()) != 1 || reqSession.cookieDirty {
		t.Fatalf("重试后应只写入一个 Cookie: cookies=%d cookieDirty=%t", len(recorder.Result().Cookies()), reqSession.cookieDirty)
	}
}

// TestDestroyReturnsRevocationErrorWithoutChangingLocalState 验证撤销失败时不会谎称会话已销毁。
func TestDestroyReturnsRevocationErrorWithoutChangingLocalState(t *testing.T) {
	backendErr := errors.New("revoke failed")
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	recorder := httptest.NewRecorder()
	first, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/", nil), recorder)
	if err != nil {
		t.Fatalf("初始化 Session 失败: %v", err)
	}
	if err = first.Set("uid", 1); err != nil {
		t.Fatalf("设置 Session 失败: %v", err)
	}
	if err = first.Save(); err != nil {
		t.Fatalf("保存 Session 失败: %v", err)
	}
	driver.updateErr = backendErr
	if err = first.Destroy(); !errors.Is(err, backendErr) {
		t.Fatalf("撤销错误应传播，实际为 %v", err)
	}
	if first.destroyed {
		t.Fatal("撤销失败后不得把本地状态标记为已销毁")
	}
}
