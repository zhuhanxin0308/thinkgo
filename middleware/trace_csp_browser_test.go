package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/debug"
)

const traceBrowserVerificationScript = `
const traceViolations = [], traceConsole = [];
document.addEventListener('securitypolicyviolation', event => traceViolations.push(event.violatedDirective));
const originalTraceLog = console.log;
console.log = function(...args) { traceConsole.push(args[0]); originalTraceLog.apply(console, args); };
document.addEventListener('DOMContentLoaded', function() {
  let failure = '';
  try {
    if (location.pathname === '/console') {
      if (!traceConsole.some(value => value && JSON.stringify(value).includes('csp-console-fixture'))) throw Error('console trace missing');
    } else {
      const toggle = document.getElementById('tg-bar-toggle');
      const container = document.getElementById('tg-bar-container');
      toggle.click();
      if (getComputedStyle(container).display !== 'flex') throw Error('panel did not open');
      const sqlTab = Array.from(document.querySelectorAll('.tg-tab')).find(tab => tab.textContent.startsWith('SQL'));
      sqlTab.click();
      if (!document.getElementById('tg-panel-sql').classList.contains('active')) throw Error('tab did not switch');
      document.querySelector('.tg-tab-close').click();
      if (getComputedStyle(container).display !== 'none') throw Error('panel did not close');
      toggle.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter'}));
      if (getComputedStyle(container).display !== 'flex' || toggle.getAttribute('aria-expanded') !== 'true') throw Error('keyboard did not open panel');
      document.querySelector('.tg-tab-close').dispatchEvent(new KeyboardEvent('keydown', {key: ' '}));
      if (getComputedStyle(container).display !== 'none') throw Error('keyboard did not close panel');
    }
    if (window.traceInjected) throw Error('trace data executed');
  } catch (error) { failure = error.message; }
  setTimeout(function() {
    const result = document.createElement('pre'); result.id = 'trace-test-result';
    result.textContent = JSON.stringify({failure, violations: traceViolations}); document.body.appendChild(result);
  }, 0);
});
`

// TestTraceBrowserDefaultCSP 在真实无头浏览器验证默认安全头、面板交互与 Console 输出。
func TestTraceBrowserDefaultCSP(t *testing.T) {
	browser := traceTestBrowser(t)
	for _, mode := range []string{"html", "console"} {
		t.Run(mode, func(t *testing.T) {
			security, err := NewSecurityHeaders(DefaultSecurityHeadersConfig())
			if err != nil {
				t.Fatal(err)
			}
			trace := &Trace{Debug: &debug.Debug{Enabled: true}, Type: mode}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, raw *http.Request) {
				request := fwcontext.MustNewRequest(raw)
				response := security(request, func(request *fwcontext.Request) *fwcontext.Response {
					return trace.Handle(request, func(request *fwcontext.Request) *fwcontext.Response {
						if raw.URL.Path == "/trace-csp-test.js" {
							return fwcontext.NewResponse().ContentType("application/javascript", "utf-8").Content(traceBrowserVerificationScript)
						}
						debug.FromRequest(request).AddLog("info", "csp-console-fixture")
						debug.FromRequest(request).AddVar("payload", `</script><script>window.traceInjected=true</script>`)
						return fwcontext.NewResponse().ContentType("text/html", "utf-8").Content(`<!doctype html><html><head><script src="/trace-csp-test.js"></script></head><body><h1>Trace fixture</h1></body></html>`)
					})
				})
				if err := response.Send(writer); err != nil {
					t.Errorf("发送 Trace 浏览器测试响应失败: %v", err)
				}
			}))
			defer server.Close()
			probe, err := server.Client().Get(server.URL + "/" + mode)
			if err != nil {
				t.Fatal(err)
			}
			_ = probe.Body.Close()
			if got := probe.Header.Get("Content-Security-Policy"); got != DefaultSecurityHeadersConfig().ContentSecurityPolicy {
				t.Fatalf("Trace 改变了默认 CSP: %q", got)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			arguments := []string{"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
				"--disable-background-networking", "--user-data-dir=" + t.TempDir(), "--dump-dom", "--virtual-time-budget=1000"}
			if runtime.GOOS == "linux" {
				arguments = append(arguments, "--no-sandbox")
			}
			arguments = append(arguments, server.URL+"/"+mode)
			// #nosec G204 -- 浏览器只来自显式测试配置或固定安装路径，参数独立且页面仅为本测试服务。
			process := exec.CommandContext(ctx, browser, arguments...)
			var diagnostic bytes.Buffer
			process.Stderr = &diagnostic
			document, err := process.Output()
			if err != nil {
				t.Fatalf("浏览器执行失败: %v\n%s", err, diagnostic.String())
			}
			_, result, found := strings.Cut(string(document), `<pre id="trace-test-result">`)
			result, _, ended := strings.Cut(result, "</pre>")
			if !found || !ended {
				t.Fatalf("浏览器未产生验收结果: %s", document)
			}
			var report struct {
				Failure    string   `json:"failure"`
				Violations []string `json:"violations"`
			}
			if err := json.Unmarshal([]byte(html.UnescapeString(result)), &report); err != nil {
				t.Fatal(err)
			}
			if report.Failure != "" || len(report.Violations) != 0 {
				t.Fatalf("默认 CSP 浏览器验收失败: %+v", report)
			}
		})
	}
}

func traceTestBrowser(t *testing.T) string {
	t.Helper()
	if configured := os.Getenv("THINKGO_TEST_CHROMIUM"); configured != "" {
		return configured
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "chrome", "msedge"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
		if root == "" {
			continue
		}
		path := filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	t.Skip("未找到 Chromium；可用 THINKGO_TEST_CHROMIUM 指定浏览器路径")
	return ""
}
