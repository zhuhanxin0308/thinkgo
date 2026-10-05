package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/debug"
)

func TestIssue18TraceRequiresLocalSource(t *testing.T) {
	for _, traceType := range []string{"html", "console"} {
		for _, tc := range []struct {
			name, peer, header, value string
			allowed                   bool
		}{
			{"remote", "203.0.113.8:4444", "", "", false},
			{"spoofed", "203.0.113.8:4444", "X-Forwarded-For", "127.0.0.1", false},
			{"proxy", "127.0.0.1:4444", "X-Forwarded-For", "203.0.113.8", false},
			{"mixed_proxy_chain", "127.0.0.1:4444", "X-Forwarded-For", "127.0.0.1, 203.0.113.8", false},
			{"forwarded", "127.0.0.1:4444", "Forwarded", "for=203.0.113.8", false},
			{"unknown", "127.0.0.1:4444", "X-Real-IP", "unknown", false},
			{"local", "127.0.0.1:4444", "", "", true},
			{"ipv6", "[::1]:4444", "", "", true},
		} {
			t.Run(traceType+"/"+tc.name, func(t *testing.T) {
				raw := httptest.NewRequest(http.MethodGet, "http://localhost/page", nil)
				raw.RemoteAddr = tc.peer
				if tc.header != "" {
					raw.Header.Set(tc.header, tc.value)
				}
				req := fwcontext.MustNewRequest(raw)
				trace := &Trace{Debug: &debug.Debug{Enabled: true}, Type: traceType}
				calls := 0
				response := trace.Handle(req, func(request *fwcontext.Request) *fwcontext.Response {
					calls++
					collector := debug.FromRequest(request)
					if (collector != nil) != tc.allowed {
						t.Errorf("collector attached=%v want=%v", collector != nil, tc.allowed)
					}
					if collector != nil {
						collector.AddSql("SELECT trace_private_marker", time.Millisecond)
					}
					return fwcontext.NewResponse().ContentType("text/html", "utf-8").Content("<html><body>public</body></html>")
				})
				if calls != 1 || response == nil {
					t.Fatal("downstream contract changed")
				}
				body := string(response.GetBody())
				if strings.Contains(body, "trace_private_marker") != tc.allowed {
					t.Fatalf("debug visibility did not match source policy: %s", body)
				}
				if tc.allowed && response.Headers().Get("Cache-Control") != "no-store" {
					t.Fatal("debug response is cacheable")
				}
				if !tc.allowed && body != "<html><body>public</body></html>" {
					t.Fatal("remote response changed")
				}
			})
		}
	}
}

func TestIssue18TraceOnlyModifiesExplicitHTML(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/json", "application/xhtml+xml", "text/htmlish", "text/html; invalid", "text/html; charset=utf-8"} {
		t.Run(contentType, func(t *testing.T) {
			trace := &Trace{Debug: &debug.Debug{Enabled: true}}
			original := fwcontext.NewResponse().Content("<html><body>public</body></html>")
			original.Header("Content-Type", contentType)
			if original.GetHeader("Content-Type") != contentType {
				t.Fatal("test did not configure the actual response header")
			}
			response := trace.Handle(newLocalTraceRequest(http.MethodGet, "http://localhost/page"), func(*fwcontext.Request) *fwcontext.Response { return original })
			want := contentType == "text/html; charset=utf-8"
			if strings.Contains(string(response.GetBody()), "tg-debug-bar") != want {
				t.Fatalf("unexpected trace output for %q", contentType)
			}
			if !want && string(response.GetBody()) != "<html><body>public</body></html>" {
				t.Fatal("non-HTML body changed")
			}
			if response != original {
				t.Fatal("response identity changed")
			}
		})
	}
}

func TestIssue18RemoteTraceAssetsFallThrough(t *testing.T) {
	for _, asset := range []string{traceScriptPath, traceStylesheetPath} {
		raw := httptest.NewRequest(http.MethodGet, "http://localhost"+asset, nil)
		raw.RemoteAddr = "203.0.113.8:4444"
		raw.Header.Set("X-Forwarded-For", "127.0.0.1")
		trace := &Trace{Debug: &debug.Debug{Enabled: true}}
		calls := 0
		response := trace.Handle(fwcontext.MustNewRequest(raw), func(req *fwcontext.Request) *fwcontext.Response {
			calls++
			if debug.FromRequest(req) != nil {
				t.Fatal("remote asset request attached collector")
			}
			return fwcontext.NewResponse().Code(http.StatusNotFound)
		})
		if calls != 1 || response.GetStatus() != http.StatusNotFound {
			t.Fatal("remote debug asset exposed")
		}
	}
}
