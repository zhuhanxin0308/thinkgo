package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponseFromRecorderPreservesSingleRenderedContentType(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	recorder.WriteHeader(http.StatusInternalServerError)
	_, _ = recorder.Write([]byte(`{"title":"failure"}`))

	response := responseFromRecorder(recorder)
	values := response.Headers().Values("Content-Type")
	if len(values) != 1 || values[0] != "application/problem+json; charset=utf-8" {
		t.Fatalf("恢复响应不得追加错误的默认 Content-Type: %#v", values)
	}
}
