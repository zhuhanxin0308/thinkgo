package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestCompressionRejectionsCompleteOnTheWire 验证协商拒绝不是传输故障，HTTP/1 与 HTTP/2 均完整结束。
func TestCompressionRejectionsCompleteOnTheWire(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		for _, scenario := range []string{"small", "large", "flush", "empty-flush", "reset-content"} {
			t.Run(scenario+map[bool]string{false: "/http1", true: "/http2"}[http2], func(t *testing.T) {
				app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": true, "min_size": 1024})
				router := mustHTTPRoute(t, app)
				var handler interface{} = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					writer.Header().Set("Cache-Control", "no-transform")
					writer.Header().Set("Content-Type", "text/plain")
					if scenario == "empty-flush" {
						_ = http.NewResponseController(writer).Flush()
					}
					body := "ok"
					if scenario == "large" {
						body = strings.Repeat(body, 1024)
					}
					_, _ = io.WriteString(writer, body)
					if scenario == "flush" {
						_ = http.NewResponseController(writer).Flush()
					}
				})
				want := http.StatusNotAcceptable
				if scenario == "reset-content" {
					want = http.StatusResetContent
					handler = func(*fwcontext.Request) *fwcontext.Response {
						return fwcontext.NewResponse().Code(http.StatusResetContent)
					}
				}
				if _, err := router.Get("/test", handler); err != nil {
					t.Fatal(err)
				}
				server := httptest.NewUnstartedServer(newTestHTTPHandler(t, app))
				server.EnableHTTP2 = http2
				if http2 {
					server.StartTLS()
				} else {
					server.Start()
				}
				defer server.Close()
				request, err := http.NewRequest(http.MethodGet, server.URL+"/test", nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Accept-Encoding", "gzip,identity;q=0")
				response, err := server.Client().Do(request)
				if err != nil {
					t.Fatalf("合法拒绝响应被中断: %v", err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil || response.StatusCode != want || len(body) != 0 || response.Header.Get("Content-Encoding") != "" {
					t.Fatalf("无实体响应错误: status=%d headers=%v bytes=%d err=%v", response.StatusCode, response.Header, len(body), err)
				}
				if http2 && response.ProtoMajor != 2 {
					t.Fatal("未实际使用 HTTP/2")
				}
			})
		}
	}
}
