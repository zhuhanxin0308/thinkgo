package session

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type sessionErrorDriver struct {
	writeErr   error
	writeCount int
}

func (d *sessionErrorDriver) Read(id string) (string, error) {
	return "", nil
}

func (d *sessionErrorDriver) Write(id string, data string) error {
	d.writeCount++
	return d.writeErr
}

func (d *sessionErrorDriver) Delete(id string) error {
	return nil
}

func (d *sessionErrorDriver) Clear() error {
	return nil
}

type sessionLogRecord struct {
	msg string
	ctx map[string]interface{}
}

type sessionTestLogger struct {
	errorCalls []sessionLogRecord
}

func (l *sessionTestLogger) ErrorCtx(msg string, ctx map[string]interface{}) {
	l.errorCalls = append(l.errorCalls, sessionLogRecord{msg: msg, ctx: ctx})
}

// TestSaveReturnsDriverErrorAndLogsIt 验证 Session 持久化失败会返回错误并写日志。
func TestSaveReturnsDriverErrorAndLogsIt(t *testing.T) {
	driver := &sessionErrorDriver{writeErr: errors.New("disk full")}
	logger := &sessionTestLogger{}
	manager := newTestSessionManager(driver, map[string]interface{}{
		"name":   "PHPSESSID",
		"expire": 600,
	}, map[string]interface{}{})
	manager.SetLogger(logger)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	recorder := httptest.NewRecorder()
	reqSession := manager.NewRequestSession(req, recorder)
	reqSession.Set("user_id", 1)
	reqSession.SetResponseWriter(recorder)

	err := reqSession.Save()
	if err == nil {
		t.Fatal("驱动写入失败时 Save 应返回错误")
	}
	if driver.writeCount != 1 {
		t.Fatalf("驱动写入应执行 1 次，实际为 %d", driver.writeCount)
	}
	if len(logger.errorCalls) != 1 {
		t.Fatalf("驱动写入失败应记录 1 条错误日志，实际为 %d", len(logger.errorCalls))
	}
}

// TestSaveReturnsMarshalErrorBeforeDriverWrite 验证 Session 序列化失败时不会继续写存储层。
func TestSaveReturnsMarshalErrorBeforeDriverWrite(t *testing.T) {
	driver := &sessionErrorDriver{}
	logger := &sessionTestLogger{}
	manager := newTestSessionManager(driver, map[string]interface{}{
		"name":   "PHPSESSID",
		"expire": 600,
	}, map[string]interface{}{})
	manager.SetLogger(logger)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	recorder := httptest.NewRecorder()
	reqSession := manager.NewRequestSession(req, recorder)
	reqSession.Set("stream", make(chan int))
	reqSession.SetResponseWriter(recorder)

	err := reqSession.Save()
	if err == nil {
		t.Fatal("序列化失败时 Save 应返回错误")
	}
	if driver.writeCount != 0 {
		t.Fatalf("序列化失败后不应继续写入驱动，实际写入 %d 次", driver.writeCount)
	}
	if len(logger.errorCalls) != 1 {
		t.Fatalf("序列化失败应记录 1 条错误日志，实际为 %d", len(logger.errorCalls))
	}
}
