package http

import (
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	sessionDriver "github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

// 检查实际提交的响应头，而不是提交后仍可修改的 Recorder.Header。
func TestCSRFHTTPCommitRoundTrip(t *testing.T) {
	for _, compression := range []struct {
		name    string
		enabled bool
		minSize int
	}{{"plain", false, 0}, {"buffered", true, 1024}, {"compressed", true, 1}} {
		for _, kind := range []string{"standard", "empty", "framework", "stream", "head"} {
			t.Run(fmt.Sprintf("%s/%s", compression.name, kind), func(t *testing.T) {
				app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{
					"enable": compression.enabled, "min_size": compression.minSize,
				})
				router := mustHTTPRoute(t, app)
				calls := 0
				var endpoint interface{}
				if kind == "framework" || kind == "stream" {
					endpoint = func(*fwcontext.Request) *fwcontext.Response {
						calls++
						response := fwcontext.NewResponse().AddHeader("Set-Cookie", "business=kept; Path=/")
						if kind == "stream" {
							return response.Stream(func(writer io.Writer) error {
								_, err := io.WriteString(writer, "ok")
								return err
							})
						}
						return response.Content("ok")
					}
				} else {
					endpoint = stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
						calls++
						writer.Header().Add("Set-Cookie", "business=kept; Path=/")
						if kind != "empty" {
							writer.WriteHeader(stdhttp.StatusOK)
							_, _ = io.WriteString(writer, "ok")
						}
					})
				}
				if _, err := router.Any("/csrf-commit", endpoint); err != nil {
					t.Fatal(err)
				}
				handler, err := NewHttp(app)
				if err != nil {
					t.Fatal(err)
				}
				installSessionMutationPipeline(t, handler, newHTTPCommitSessionManager(t, sessionDriver.NewMemory()))
				config := middleware.DefaultCSRFConfig()
				config.Secret = strings.Repeat("c", 32)
				csrf, err := middleware.CsrfWithConfig(config)
				if err != nil {
					t.Fatal(err)
				}
				handler.middleware.Pipe(csrf)

				method := stdhttp.MethodGet
				if kind == "head" {
					method = stdhttp.MethodHead
				}
				raw := httptest.NewRequest(method, "http://127.0.0.1/csrf-commit", nil)
				raw.Header.Set("Accept-Encoding", "gzip")
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, raw)
				result := recorder.Result()
				defer result.Body.Close()
				if result.StatusCode != stdhttp.StatusOK {
					t.Fatalf("initial response = %d: %s", result.StatusCode, recorder.Body.String())
				}
				if kind == "head" && recorder.Body.Len() != 0 {
					t.Fatal("HEAD response must not contain an entity")
				}
				var token *stdhttp.Cookie
				counts := make(map[string]int)
				for _, cookie := range result.Cookies() {
					counts[cookie.Name]++
					if cookie.Name == config.CookieName {
						token = cookie
					}
				}
				if token == nil || token.Value == "" || counts[config.CookieName] != 1 || counts["business"] != 1 || counts["COMMITSESSID"] != 1 {
					t.Fatalf("committed cookies must coexist without duplication: %v", result.Header.Values("Set-Cookie"))
				}

				post := httptest.NewRequest(stdhttp.MethodPost, "http://127.0.0.1/csrf-commit", nil)
				post.AddCookie(token)
				post.Header.Set(config.HeaderName, token.Value)
				post.Header.Set("Origin", "http://127.0.0.1")
				accepted := httptest.NewRecorder()
				handler.ServeHTTP(accepted, post)
				if accepted.Result().StatusCode != stdhttp.StatusOK || calls != 2 {
					t.Fatalf("issued token must authorize POST: status=%d calls=%d", accepted.Code, calls)
				}

				invalid := httptest.NewRequest(stdhttp.MethodPost, "http://127.0.0.1/csrf-commit", nil)
				invalid.AddCookie(token)
				invalid.Header.Set(config.HeaderName, "invalid")
				invalid.Header.Set("Origin", "http://127.0.0.1")
				rejected := httptest.NewRecorder()
				handler.ServeHTTP(rejected, invalid)
				if rejected.Result().StatusCode != stdhttp.StatusForbidden || calls != 2 {
					t.Fatalf("invalid token must not reach endpoint: status=%d calls=%d", rejected.Code, calls)
				}
			})
		}
	}
}
