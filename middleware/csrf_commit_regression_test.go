package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func TestCSRFDirectWriterGetsCookieBeforeCommit(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("s", 32)
	handler, err := CsrfWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	raw := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	request := fwcontext.MustNewRequest(raw, fwcontext.WithResponseWriter(recorder))
	response := handler(request, func(*fwcontext.Request) *fwcontext.Response {
		recorder.Header().Add("Set-Cookie", "business=kept; Path=/")
		recorder.WriteHeader(http.StatusCreated)
		return fwcontext.NewCommittedResponse(http.StatusCreated)
	})
	if !response.Committed() || recorder.Result().StatusCode != http.StatusCreated {
		t.Fatal("direct response contract changed")
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 || cookies[0].Name != config.CookieName || !cookies[0].Secure {
		t.Fatalf("cookie was not committed before the handler: %v", recorder.Result().Header)
	}
}

type csrfRejectingCommitWriter struct{ *httptest.ResponseRecorder }

func (*csrfRejectingCommitWriter) BeforeCommit(func(http.Header) error) error {
	return errors.New("response already committed")
}

func TestCSRFFailedCommitRegistrationStopsDownstream(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("s", 32)
	handler, err := CsrfWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	writer := &csrfRejectingCommitWriter{httptest.NewRecorder()}
	request := fwcontext.MustNewRequest(
		httptest.NewRequest(http.MethodGet, "http://example.com/", nil),
		fwcontext.WithResponseWriter(writer),
	)
	response := handler(request, func(*fwcontext.Request) *fwcontext.Response {
		t.Fatal("failed hook registration must stop downstream execution")
		return nil
	})
	if response.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", response.GetStatus())
	}
}

func TestCSRFFailedTokenGenerationStopsDownstream(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("s", 32)
	middleware := &csrfMiddleware{
		config:      config,
		safeMethods: map[string]struct{}{http.MethodGet: {}},
		random:      failingCSRFReader{err: io.ErrUnexpectedEOF},
		now:         nowForCSRFTest,
	}
	response := middleware.handle(
		fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil)),
		func(*fwcontext.Request) *fwcontext.Response {
			t.Fatal("failed token generation must stop downstream execution")
			return nil
		},
	)
	if response.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", response.GetStatus())
	}
}
