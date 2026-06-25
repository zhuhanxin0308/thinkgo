package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/debug"
)

// newLocalTraceRequest 构造一个来自本机回环地址的请求，使其满足 Trace 调试条的暴露门禁。
func newLocalTraceRequest(method, target string) *fwcontext.Request {
	raw := httptest.NewRequest(method, target, nil)
	raw.RemoteAddr = "127.0.0.1:54321"
	return fwcontext.NewRequest(raw)
}

// TestTraceEscapesDebugPanelContent 验证调试面板会把危险内容转义后再输出到 HTML。
func TestTraceEscapesDebugPanelContent(t *testing.T) {
	trace := &Trace{Debug: &debug.Debug{Enabled: true}}
	req := newLocalTraceRequest(http.MethodGet, "http://example.com/search?q=ok")

	resp := trace.Handle(req, func(req *fwcontext.Request) *fwcontext.Response {
		reqDebug, ok := req.GetData("_debug").(*debug.Debug)
		if !ok || reqDebug == nil {
			t.Fatal("Trace 中间件应向请求上下文写入调试实例")
		}

		reqDebug.AddLog("info", `<img src=x onerror=alert(1)>`)
		reqDebug.AddSql(`SELECT '<script>alert(1)</script>'`, 10*time.Millisecond)
		reqDebug.AddCache("GET", `<svg onload=alert(1)>`)
		reqDebug.AddVar("payload", `<script>alert(1)</script>`)
		reqDebug.AddFile(`<iframe src=javascript:alert(1)>`)

		return fwcontext.NewResponse().Content("<html><body>ok</body></html>")
	})

	body := string(resp.GetBody())
	rawFragments := []string{
		`<img src=x onerror=alert(1)>`,
		`<script>alert(1)</script>`,
		`<svg onload=alert(1)>`,
		`<iframe src=javascript:alert(1)>`,
	}
	for _, fragment := range rawFragments {
		if strings.Contains(body, fragment) {
			t.Fatalf("调试面板不应把危险片段原样写入 HTML，命中片段 %q，响应为 %s", fragment, body)
		}
	}

	escapedFragments := []string{
		`&lt;img src=x onerror=alert(1)&gt;`,
		`&lt;script&gt;alert(1)&lt;/script&gt;`,
		`&lt;svg onload=alert(1)&gt;`,
		`&lt;iframe src=javascript:alert(1)&gt;`,
	}
	for _, fragment := range escapedFragments {
		if !strings.Contains(body, fragment) {
			t.Fatalf("调试面板应输出已转义文本，缺少片段 %q，响应为 %s", fragment, body)
		}
	}
}

// TestTraceInjectsExternalAssetsInsteadOfInlineBundle 验证调试面板改为注入外部静态资源，而不是每次响应都拼接整段内联样式和脚本。
func TestTraceInjectsExternalAssetsInsteadOfInlineBundle(t *testing.T) {
	trace := &Trace{Debug: &debug.Debug{Enabled: true}}
	req := newLocalTraceRequest(http.MethodGet, "http://example.com/debug-page")

	resp := trace.Handle(req, func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("<html><body>ok</body></html>")
	})

	body := string(resp.GetBody())
	if !strings.Contains(body, `href="/__thinkgo_debug__/trace.css"`) {
		t.Fatalf("调试面板应注入外部样式资源，响应内容为 %s", body)
	}
	if !strings.Contains(body, `src="/__thinkgo_debug__/trace.js"`) {
		t.Fatalf("调试面板应注入外部脚本资源，响应内容为 %s", body)
	}
	if strings.Contains(body, "#tg-debug-bar {") {
		t.Fatalf("调试面板不应继续把完整样式内联到每个响应中，响应内容为 %s", body)
	}
	if strings.Contains(body, "var TgDebug = {") {
		t.Fatalf("调试面板不应继续把完整脚本内联到每个响应中，响应内容为 %s", body)
	}
}

// TestTraceNotExposedToRemoteRequests 验证即便开启 Trace，非本机回环请求也不会被注入调试条，
// 避免向远程客户端泄露 SQL、客户端 IP、耗时等内部信息。
func TestTraceNotExposedToRemoteRequests(t *testing.T) {
	trace := &Trace{Debug: &debug.Debug{Enabled: true}}
	// httptest 默认 RemoteAddr 为 192.0.2.1（非回环）。
	req := fwcontext.NewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/page", nil))

	resp := trace.Handle(req, func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("<html><body>ok</body></html>")
	})

	body := string(resp.GetBody())
	if strings.Contains(body, "tg-debug-bar") || strings.Contains(body, "__thinkgo_debug__") {
		t.Fatalf("远程请求不应被注入调试条，响应为 %s", body)
	}
}

// TestTraceAssetsNotServedToRemoteRequests 验证调试静态资源端点对远程请求不可用。
func TestTraceAssetsNotServedToRemoteRequests(t *testing.T) {
	trace := &Trace{Debug: &debug.Debug{Enabled: true}}
	req := fwcontext.NewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/__thinkgo_debug__/trace.css", nil))

	nextCalled := false
	trace.Handle(req, func(req *fwcontext.Request) *fwcontext.Response {
		nextCalled = true
		return fwcontext.NewResponse().Content("business")
	})
	if !nextCalled {
		t.Fatal("远程请求访问调试资源端点时应继续进入业务链路（即资源不对外暴露）")
	}
}

// TestTraceServesStaticAssets 验证 Trace 中间件会直接返回调试静态资源，避免经过业务处理链重复拼接。
func TestTraceServesStaticAssets(t *testing.T) {
	trace := &Trace{Debug: &debug.Debug{Enabled: true}}
	nextCalled := false

	cases := []struct {
		name        string
		path        string
		contentType string
		expected    string
	}{
		{
			name:        "css",
			path:        "/__thinkgo_debug__/trace.css",
			contentType: "text/css; charset=utf-8",
			expected:    "#tg-debug-bar",
		},
		{
			name:        "js",
			path:        "/__thinkgo_debug__/trace.js",
			contentType: "application/javascript; charset=utf-8",
			expected:    "window.TgDebug",
		},
	}

	for _, tc := range cases {
		req := newLocalTraceRequest(http.MethodGet, "http://example.com"+tc.path)
		resp := trace.Handle(req, func(req *fwcontext.Request) *fwcontext.Response {
			nextCalled = true
			return fwcontext.NewResponse().Content("unexpected")
		})

		if nextCalled {
			t.Fatalf("%s 资源请求应由 Trace 中间件直接处理，不应继续进入业务链路", tc.name)
		}
		if got := resp.Headers().Get("Content-Type"); got != tc.contentType {
			t.Fatalf("%s 资源 Content-Type 错误，期望 %q，实际为 %q", tc.name, tc.contentType, got)
		}
		if body := string(resp.GetBody()); !strings.Contains(body, tc.expected) {
			t.Fatalf("%s 资源内容错误，期望包含 %q，实际为 %s", tc.name, tc.expected, body)
		}
	}
}
