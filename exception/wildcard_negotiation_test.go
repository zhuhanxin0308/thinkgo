package exception

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWildcardAcceptUsesRequestIntent(t *testing.T) {
	for _, intent := range []struct{ name, path, xhr, contentType string }{
		{"API", "/api/orders", "", ""},
		{"XHR", "/orders", "XMLHttpRequest", ""},
		{"JSONBody", "/orders", "", "application/json"},
		{"HTML", "/orders", "", ""},
	} {
		for _, accept := range []struct {
			value string
			mode  int
		}{
			{"", 0}, {"*/*", 0}, {"*/*;q=0.5", 0},
			{"application/json", 1}, {"text/html", -1},
			{"application/json;q=0,*/*;q=1", -1},
			{"application/json;q=0.2,text/html;q=0.8", -1},
			{"application/json;q=0.8,text/html;q=0.2", 1},
			{"text/html,application/json", -1}, {"application/json,text/html", 1},
			{"*/*;q=0", -1}, {"image/png", -1}, {"*/*;q=NaN", -1},
			{"application/json;q=0.5,*/*;q=1", -1},
			{"text/plain;q=1,*/*;q=0.1", -1},
			{"text/plain;q=0.5,*/*;q=0.5", -1},
			{"text/plain;q=0.1,*/*;q=0.9", 0},
			{"text/plain;q=0,*/*;q=0.5", 0},
			{"text/plain;q=0.9,application/json;q=0.5", -1},
			{"text/plain;q=0.5,application/json;q=0.9", 1},
		} {
			t.Run(intent.name+"/"+accept.value, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "http://example.com"+intent.path, nil)
				req.Header.Set("Accept", accept.value)
				req.Header.Set("X-Requested-With", intent.xhr)
				req.Header.Set("Content-Type", intent.contentType)
				want := accept.mode == 1 || (accept.mode == 0 && intent.name != "HTML")
				if got := (&Handle{}).isJSONRequest(req); got != want {
					t.Fatalf("协商 JSON=%v，期望 %v", got, want)
				}
			})
		}
	}
}
