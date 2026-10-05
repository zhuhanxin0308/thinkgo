package session

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type issueCaptureLogger struct {
	message string
	fields  map[string]interface{}
}

func (l *issueCaptureLogger) ErrorCtx(message string, fields map[string]interface{}) {
	l.message = message
	l.fields = fields
}
func TestIssue42CustomLoggerNeverReceivesSessionID(t *testing.T) {
	logger := &issueCaptureLogger{}
	session := &Session{logger: logger}
	id := strings.Repeat("a", 32)
	cause := errors.New("read " + id + " password=secret-value failed")
	fields := map[string]interface{}{"session_id": id, "operation": "read"}
	if got := session.reportError("session failed", cause, fields); got != cause {
		t.Fatal("error identity lost")
	}
	exposed := logger.message + fmt.Sprint(logger.fields)
	if strings.Contains(exposed, id) || strings.Contains(exposed, "secret-value") {
		t.Fatalf("credentials passed to custom logger: %s", exposed)
	}
	if fields["session_id"] != id || logger.fields["operation"] != "read" {
		t.Fatal("caller metadata mutated or ordinary metadata lost")
	}
}

func TestIssue42CurrentIDRedactedWithoutMetadata(t *testing.T) {
	id := strings.Repeat("b", 32)
	logger := &issueCaptureLogger{}
	session := &Session{logger: logger, id: id}
	cause := errors.New("cookie write " + id + " failed")
	if got := session.reportError("flush", cause, nil); got != cause {
		t.Fatal("error identity changed")
	}
	if strings.Contains(logger.message, id) || !strings.Contains(logger.message, "[REDACTED]") {
		t.Fatal("current ID exposed without metadata")
	}
}

func TestIssue42InitializationErrorSanitizedBeforeCustomLogger(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	cause := errors.New("backend read " + id + " failed")
	driver := newCountingDriver()
	driver.readErr = cause
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	logger := &issueCaptureLogger{}
	manager.SetLogger(logger)
	raw := httptest.NewRequest(http.MethodGet, "/", nil)
	raw.AddCookie(&http.Cookie{Name: "SID", Value: id})
	if _, err := manager.NewRequestSession(raw, httptest.NewRecorder()); !errors.Is(err, cause) {
		t.Fatalf("error contract changed: %v", err)
	}
	if logger.message == "" || strings.Contains(logger.message+fmt.Sprint(logger.fields), id) {
		t.Fatal("initialization leaked ID before custom logger")
	}
	if logger.fields["component"] != "session" {
		t.Fatal("diagnostic component lost")
	}
}
