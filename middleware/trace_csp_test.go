package middleware

import (
	"net/http"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/debug"
)

// TestTraceCSPAssetsRemainLocalOnly 验证两种输出模式都保留本机调试暴露边界。
func TestTraceCSPAssetsRemainLocalOnly(t *testing.T) {
	for _, mode := range []string{"html", "console"} {
		trace := &Trace{Debug: &debug.Debug{Enabled: true}, Type: mode}
		for _, path := range []string{"/page", traceScriptPath, traceStylesheetPath} {
			request := newLocalTraceRequest(http.MethodGet, "http://example.com"+path)
			request.Raw().RemoteAddr = "198.51.100.20:1234"
			response := trace.Handle(request, func(*fwcontext.Request) *fwcontext.Response {
				return fwcontext.NewResponse().Code(http.StatusNotFound).ContentType("text/html", "utf-8").Content("<html><body>not found</body></html>")
			})
			if response.GetStatus() != http.StatusNotFound || strings.Contains(string(response.GetBody()), "tg-") {
				t.Fatalf("远程请求获得调试内容: mode=%s path=%s", mode, path)
			}
		}
	}
}
