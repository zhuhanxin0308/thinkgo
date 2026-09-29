package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type csrfCommitTestWriter struct {
	*httptest.ResponseRecorder
	hooks []func(http.Header) error
	err   error
}

func (w *csrfCommitTestWriter) BeforeCommit(hook func(http.Header) error) error {
	if w.err != nil {
		return w.err
	}
	w.hooks = append(w.hooks, hook)
	return nil
}

func (w *csrfCommitTestWriter) WriteHeader(code int) {
	for _, hook := range w.hooks {
		if err := hook(w.Header()); err != nil {
			panic(err)
		}
	}
	w.hooks = nil
	w.ResponseRecorder.WriteHeader(code)
}

func TestCSRFCookieRegisteredBeforeFinalCommit(t *testing.T) {
	writer := &csrfCommitTestWriter{ResponseRecorder: httptest.NewRecorder()}
	if err := writeCSRFCookieBeforeCommit(writer, "csrf_token=test; Path=/"); err != nil {
		t.Fatal(err)
	}
	if len(writer.hooks) != 1 || writer.Header().Get("Set-Cookie") != "" {
		t.Fatal("hook must be registered without prematurely modifying final headers")
	}
	writer.WriteHeader(http.StatusOK)
	result := writer.Result()
	defer result.Body.Close()
	if len(result.Cookies()) != 1 || result.Cookies()[0].Value != "test" {
		t.Fatalf("cookie missing/duplicated on wire: %v", result.Header)
	}
}

func TestCSRFCookiePlainWriterAndRegistrationError(t *testing.T) {
	writer := httptest.NewRecorder()
	if err := writeCSRFCookieBeforeCommit(writer, "csrf_token=test; Path=/"); err != nil {
		t.Fatal(err)
	}
	writer.WriteHeader(http.StatusOK)
	result := writer.Result()
	defer result.Body.Close()
	if len(result.Cookies()) != 1 {
		t.Fatal("plain writer cookie was added after commit")
	}
	failure := errors.New("registration closed")
	closed := &csrfCommitTestWriter{ResponseRecorder: httptest.NewRecorder(), err: failure}
	if err := writeCSRFCookieBeforeCommit(closed, "csrf_token=test"); !errors.Is(err, failure) {
		t.Fatalf("registration error lost: %v", err)
	}
	if len(closed.hooks) != 0 || closed.Header().Get("Set-Cookie") != "" {
		t.Fatal("failed registration modified response")
	}
}
