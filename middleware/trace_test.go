package middleware

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/debug"
	frameworkVersion "github.com/zhuhanxin0308/thinkgo/v3/version"
	"github.com/zhuhanxin0308/thinkgo/v3/view"
)

var traceAllocationResponseSink *fwcontext.Response

// TestTraceDefaultsToUTC 验证 Trace 的隐式时区不依赖部署机器。
func TestTraceDefaultsToUTC(t *testing.T) {
	if location := (&Trace{}).location(); location != time.UTC {
		t.Fatalf("Trace 默认时区必须为 UTC: %v", location)
	}
}

// newLocalTraceRequest 构造一个来自本机回环地址的请求，使其满足 Trace 调试条的暴露门禁。
func newLocalTraceRequest(method, target string) *fwcontext.Request {
	raw := httptest.NewRequest(method, target, nil)
	raw.RemoteAddr = "127.0.0.1:54321"
	return fwcontext.MustNewRequest(raw)
}

// TestTraceEscapesDebugPanelContent 验证调试面板会把危险内容转义后再输出到 HTML。
func TestTraceEscapesDebugPanelContent(t *testing.T) {
	trace := &Trace{Debug: &debug.Debug{Enabled: true}}
	req := newLocalTraceRequest(http.MethodGet, "http://example.com/search?q=ok")

	resp := trace.Handle(req, func(req *fwcontext.Request) *fwcontext.Response {
		reqDebug, ok := req.GetData(debug.RequestKey).(*debug.Debug)
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
	if !strings.Contains(body, frameworkVersion.Framework) {
		t.Fatalf("调试面板应使用中心版本标签 %q，响应内容为 %s", frameworkVersion.Framework, body)
	}
}

// TestTraceFollowsThinkPHPDebugVisibility 验证开启 Debug 后 Trace 对当前 HTTP
// 请求生效，不额外改变 ThinkPHP 的来源地址语义。
func TestTraceFollowsThinkPHPDebugVisibility(t *testing.T) {
	trace := &Trace{Debug: &debug.Debug{Enabled: true}}
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/page", nil))

	resp := trace.Handle(req, func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("<html><body>ok</body></html>")
	})

	body := string(resp.GetBody())
	if !strings.Contains(body, "tg-debug-bar") || !strings.Contains(body, "__thinkgo_debug__") {
		t.Fatalf("Debug 请求应被注入 Html Trace，响应为 %s", body)
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

// TestTraceConsoleTypeInjectsBrowserConsoleScript 验证 trace.type=Console
// 使用浏览器控制台脚本，不再渲染 Html 调试条。
func TestTraceConsoleTypeInjectsBrowserConsoleScript(t *testing.T) {
	trace := &Trace{Debug: &debug.Debug{Enabled: true}, Type: "Console"}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/page", nil))
	response := trace.Handle(request, func(request *fwcontext.Request) *fwcontext.Response {
		debug.FromRequest(request).AddLog("info", "console trace")
		return fwcontext.NewResponse().Content("<html><body>ok</body></html>")
	})
	body := string(response.GetBody())
	if !strings.Contains(body, "console.group") || strings.Contains(body, "tg-debug-bar") {
		t.Fatalf("Console Trace 输出错误: %s", body)
	}
}

// TestTraceChannelFiltersRequestLogs 验证 trace.channel 只展示同名日志通道，
// 默认通道与其它通道的记录不会混入页面 Trace。
func TestTraceChannelFiltersRequestLogs(t *testing.T) {
	trace := &Trace{Debug: &debug.Debug{Enabled: true}, Channel: "sql"}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/page", nil))
	response := trace.Handle(request, func(request *fwcontext.Request) *fwcontext.Response {
		collector := debug.FromRequest(request)
		collector.AddLog("info", "default-channel")
		collector.AddLog("info", "selected-channel", "sql")
		collector.AddLog("info", "other-channel", "audit")
		return fwcontext.NewResponse().Content("<html><body>ok</body></html>")
	})
	body := string(response.GetBody())
	if !strings.Contains(body, "selected-channel") {
		t.Fatalf("Trace 缺少选中通道日志: %s", body)
	}
	if strings.Contains(body, "default-channel") || strings.Contains(body, "other-channel") {
		t.Fatalf("Trace 混入未选中通道日志: %s", body)
	}
}

type traceIsolationViewDriver struct{}

func (d *traceIsolationViewDriver) Config(map[string]interface{}) error { return nil }

func (d *traceIsolationViewDriver) Fetch(name string, _ map[string]interface{}) (string, error) {
	return name, nil
}

func (d *traceIsolationViewDriver) Display(writer io.Writer, name string, _ map[string]interface{}) error {
	_, err := io.WriteString(writer, name)
	return err
}

func (d *traceIsolationViewDriver) Exists(string) (bool, error) { return true, nil }

func (d *traceIsolationViewDriver) SetFuncMap(map[string]interface{}) error { return nil }

// TestTraceRequestIsolation 验证并发请求的缓存键和模板记录只进入各自的调试面板。
func TestTraceRequestIsolation(t *testing.T) {
	trace := &Trace{Debug: &debug.Debug{Enabled: true}}
	rootCache := cache.NewCache(nil, cacheDriver.NewMemory())
	rootView := view.NewView(nil, nil)
	if err := rootView.SetDriver(&traceIsolationViewDriver{}); err != nil {
		t.Fatalf("安装 Trace 测试视图驱动失败: %v", err)
	}

	const requestCount = 12
	type result struct {
		index int
		body  string
		err   error
	}
	results := make(chan result, requestCount)
	release := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(requestCount)

	for index := 0; index < requestCount; index++ {
		index := index
		go func() {
			request := newLocalTraceRequest(http.MethodGet, fmt.Sprintf("http://example.com/request-%d", index))
			var handlerErr error
			response := trace.Handle(request, func(request *fwcontext.Request) *fwcontext.Response {
				collector := debug.FromRequest(request)
				if collector == nil {
					handlerErr = fmt.Errorf("请求 %d 未挂载 collector", index)
					ready.Done()
					<-release
					return fwcontext.NewResponse().Content("<html><body>missing</body></html>")
				}
				cacheKey := fmt.Sprintf("cache-request-%02d", index)
				if err := rootCache.WithDebug(collector).Set(cacheKey, index, time.Minute); err != nil {
					handlerErr = fmt.Errorf("请求 %d 写缓存失败: %w", index, err)
				}
				templateName := fmt.Sprintf("template-request-%02d", index)
				if err := rootView.RenderWithDebug(collector, io.Discard, templateName, nil); err != nil {
					handlerErr = fmt.Errorf("请求 %d 渲染视图失败: %w", index, err)
				}
				ready.Done()
				<-release
				return fwcontext.NewResponse().Content("<html><body>ok</body></html>")
			})
			results <- result{index: index, body: string(response.GetBody()), err: handlerErr}
		}()
	}

	ready.Wait()
	close(release)
	for completed := 0; completed < requestCount; completed++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		ownCacheKey := fmt.Sprintf("cache-request-%02d", result.index)
		ownTemplate := fmt.Sprintf("template-request-%02d", result.index)
		if !strings.Contains(result.body, ownCacheKey) || !strings.Contains(result.body, ownTemplate) {
			t.Fatalf("请求 %d 面板缺少自己的数据，响应为 %s", result.index, result.body)
		}
		for other := 0; other < requestCount; other++ {
			if other == result.index {
				continue
			}
			if strings.Contains(result.body, fmt.Sprintf("cache-request-%02d", other)) ||
				strings.Contains(result.body, fmt.Sprintf("template-request-%02d", other)) {
				t.Fatalf("请求 %d 面板混入请求 %d 的数据，响应为 %s", result.index, other, result.body)
			}
		}
	}
}

// TestTraceDisabledRequestsDoNotAttachCollector 验证 Trace 未配置或关闭时不会创建 collector。
func TestTraceUnauthorizedRequestsDoNotAttachCollector(t *testing.T) {
	cases := []struct {
		name    string
		trace   *Trace
		request *fwcontext.Request
	}{
		{
			name:    "nil-middleware",
			trace:   nil,
			request: newLocalTraceRequest(http.MethodGet, "http://example.com/nil-middleware"),
		},
		{
			name:    "nil-config",
			trace:   &Trace{},
			request: newLocalTraceRequest(http.MethodGet, "http://example.com/nil-config"),
		},
		{
			name:    "disabled",
			trace:   &Trace{Debug: &debug.Debug{Enabled: false}},
			request: newLocalTraceRequest(http.MethodGet, "http://example.com/disabled"),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := testCase.trace.Handle(testCase.request, func(request *fwcontext.Request) *fwcontext.Response {
				if collector := debug.FromRequest(request); collector != nil {
					t.Fatalf("未授权请求不应挂载 collector，实际为 %#v", collector)
				}
				return fwcontext.NewResponse().Content("<html><body>ok</body></html>")
			})
			if collector := debug.FromRequest(testCase.request); collector != nil {
				t.Fatalf("请求结束后仍发现未授权 collector: %#v", collector)
			}
			if strings.Contains(string(response.GetBody()), "tg-debug-bar") {
				t.Fatalf("未授权请求不应注入调试面板，响应为 %s", response.GetBody())
			}
		})
	}
}

// TestTraceDisabledDoesNotAllocateCollector 验证关闭 Trace 的路径与直接调用后继处理器具有相同堆分配数。
func TestTraceDisabledDoesNotAllocateCollector(t *testing.T) {
	trace := &Trace{Debug: &debug.Debug{Enabled: false}}
	request := newLocalTraceRequest(http.MethodGet, "http://example.com/disabled")
	response := fwcontext.NewResponse().Content("plain")
	next := func(*fwcontext.Request) *fwcontext.Response { return response }

	baseline := testing.AllocsPerRun(1000, func() {
		traceAllocationResponseSink = next(request)
	})
	actual := testing.AllocsPerRun(1000, func() {
		traceAllocationResponseSink = trace.Handle(request, next)
	})
	if actual != baseline {
		t.Fatalf("关闭 Trace 不应产生 collector 额外分配，基线=%.2f，实际=%.2f", baseline, actual)
	}
}

// TestTraceDebugPanelShowsTruncation 验证调试页只消费快照并明确展示每类数据的截断状态。
func TestTraceDebugPanelShowsTruncation(t *testing.T) {
	collector := debug.NewRequestDebug(true)
	for index := 0; index <= debug.MaxLogEntries; index++ {
		collector.AddLog("info", fmt.Sprintf("log-%d", index))
	}
	for index := 0; index <= debug.MaxSQLEntries; index++ {
		collector.AddSql(fmt.Sprintf("select %d", index), time.Millisecond)
	}
	for index := 0; index <= debug.MaxCacheEntries; index++ {
		collector.AddCache("GET", fmt.Sprintf("cache-%d", index))
	}
	for index := 0; index <= debug.MaxVarEntries; index++ {
		collector.AddVar(fmt.Sprintf("var-%d", index), index)
	}
	for index := 0; index <= debug.MaxFileEntries; index++ {
		collector.AddFile(fmt.Sprintf("file-%d", index))
	}

	panel := buildDebugBar(collector.GetInfo())
	labels := []string{
		fmt.Sprintf("SQL (%d, truncated)", debug.MaxSQLEntries),
		fmt.Sprintf("Cache (%d, truncated)", debug.MaxCacheEntries),
		fmt.Sprintf("Logs (%d, truncated)", debug.MaxLogEntries),
		fmt.Sprintf("Files (%d, truncated)", debug.MaxFileEntries),
		fmt.Sprintf("Debug (%d, truncated)", debug.MaxVarEntries),
	}
	for _, label := range labels {
		if !strings.Contains(panel, label) {
			t.Fatalf("调试页缺少截断标签 %q，面板为 %s", label, panel)
		}
	}
}

// TestTraceCollectorResponseBoundaries 验证授权请求在空响应、非 HTML 和无闭合标签响应上的 collector 生命周期。
func TestTraceCollectorResponseBoundaries(t *testing.T) {
	trace := &Trace{Debug: &debug.Debug{Enabled: true}}
	tests := []struct {
		name     string
		response *fwcontext.Response
	}{
		{name: "nil-response", response: nil},
		{name: "json-response", response: fwcontext.NewResponse().Json(map[string]interface{}{"ok": true})},
		{name: "html-without-closing-tag", response: fwcontext.NewResponse().Content("plain fragment")},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := newLocalTraceRequest(http.MethodGet, "http://example.com/"+testCase.name)
			response := trace.Handle(request, func(request *fwcontext.Request) *fwcontext.Response {
				collector := debug.FromRequest(request)
				if collector == nil || !collector.Enabled {
					t.Fatalf("授权请求处理期间应挂载启用的 collector，实际为 %#v", collector)
				}
				collector.AddLog("info", testCase.name)
				return testCase.response
			})
			if response != testCase.response {
				t.Fatalf("Trace 应原样保留边界响应，期望 %#v，实际 %#v", testCase.response, response)
			}
			if testCase.name == "html-without-closing-tag" && !strings.Contains(string(response.GetBody()), "tg-debug-bar") {
				t.Fatalf("无闭合标签的 Html 响应应在末尾追加调试面板，响应为 %s", response.GetBody())
			}
			if testCase.name != "html-without-closing-tag" && response != nil && strings.Contains(string(response.GetBody()), "tg-debug-bar") {
				t.Fatalf("非 Html 边界响应不应注入调试面板，响应为 %s", response.GetBody())
			}
			collector := debug.FromRequest(request)
			if collector == nil {
				t.Fatal("授权请求结束后请求仍应拥有 collector")
			}
			if logs := collector.GetInfo()["logs"].([]map[string]interface{}); len(logs) != 0 {
				t.Fatalf("请求结束后 collector 应释放已渲染数据，实际为 %#v", logs)
			}
		})
	}
}
