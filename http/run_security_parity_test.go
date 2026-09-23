package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestDirectRunEnforcesNetworkSecurityOptions 验证直接入口同样执行 Host 和请求体限制。
func TestDirectRunEnforcesNetworkSecurityOptions(t *testing.T) {
	t.Run("拒绝未授权 Host", func(t *testing.T) {
		kernel, _ := newGeneratedSkeletonHTTP(t, "", false)
		raw := httptest.NewRequest(http.MethodGet, "http://evil.example/robots.txt", nil)
		recorder := httptest.NewRecorder()
		request := fwcontext.MustNewRequest(raw, fwcontext.WithResponseWriter(recorder))
		response := kernel.Run(request)
		defer kernel.End(response)
		if response.GetCode() != http.StatusMisdirectedRequest {
			t.Fatalf("直接 Run 未拒绝非法 Host: status=%d", response.GetCode())
		}
	})

	t.Run("限制请求体大小", func(t *testing.T) {
		kernel := newLimitedDirectRunKernel(t)
		raw := httptest.NewRequest(http.MethodPost, "http://example.com/index/index/hello", strings.NewReader(`{"value":"`+strings.Repeat("x", 142)+`"}`))
		raw.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		request := fwcontext.MustNewRequest(raw, fwcontext.WithResponseWriter(recorder))
		response := kernel.Run(request)
		defer kernel.End(response)
		if response.GetCode() != http.StatusRequestEntityTooLarge {
			t.Fatalf("直接 Run 未限制请求体: status=%d", response.GetCode())
		}
	})

	t.Run("预解析的分块请求体仍受限制", func(t *testing.T) {
		kernel := newLimitedDirectRunKernel(t)
		raw := httptest.NewRequest(http.MethodPost, "http://example.com/index/index/hello", strings.NewReader(`{"value":"`+strings.Repeat("x", 142)+`"}`))
		raw.ContentLength = -1
		raw.Header.Set("Content-Type", "application/json")
		request := fwcontext.MustNewRequest(raw, fwcontext.WithResponseWriter(httptest.NewRecorder()))
		if err := request.Parse(); err != nil {
			t.Fatal(err)
		}
		response := kernel.Run(request)
		defer kernel.End(response)
		if response.GetCode() != http.StatusRequestEntityTooLarge {
			t.Fatalf("预解析请求体绕过限制: status=%d", response.GetCode())
		}
	})

	t.Run("未预解析的分块请求体仍受限制", func(t *testing.T) {
		kernel := newLimitedDirectRunKernel(t)
		raw := httptest.NewRequest(http.MethodPost, "http://example.com/index/index/hello", strings.NewReader(`{"value":"`+strings.Repeat("x", 142)+`"}`))
		raw.ContentLength = -1
		raw.Header.Set("Content-Type", "application/json")
		request := fwcontext.MustNewRequest(raw, fwcontext.WithResponseWriter(httptest.NewRecorder()))
		response := kernel.Run(request)
		defer kernel.End(response)
		if response.GetCode() != http.StatusRequestEntityTooLarge {
			t.Fatalf("分块请求体绕过限制: status=%d", response.GetCode())
		}
	})

	t.Run("构造请求后注入的分块请求体仍受限制", func(t *testing.T) {
		kernel := newLimitedDirectRunKernel(t)
		raw := httptest.NewRequest(http.MethodPost, "http://example.com/index/index/hello", nil)
		request := fwcontext.MustNewRequest(raw, fwcontext.WithResponseWriter(httptest.NewRecorder()))
		raw.Body = io.NopCloser(strings.NewReader(`{"value":"` + strings.Repeat("x", 142) + `"}`))
		raw.ContentLength = -1
		raw.Header.Set("Content-Type", "application/json")
		response := kernel.Run(request)
		defer kernel.End(response)
		if response.GetCode() != http.StatusRequestEntityTooLarge {
			t.Fatalf("后注入的请求体绕过限制: status=%d", response.GetCode())
		}
	})

	t.Run("构造请求后注入并预解析的请求体仍受限制", func(t *testing.T) {
		kernel := newLimitedDirectRunKernel(t)
		raw := httptest.NewRequest(http.MethodPost, "http://example.com/index/index/hello", nil)
		request := fwcontext.MustNewRequest(raw, fwcontext.WithResponseWriter(httptest.NewRecorder()))
		raw.Body = io.NopCloser(strings.NewReader(`{"value":"` + strings.Repeat("x", 142) + `"}`))
		raw.ContentLength = -1
		raw.Header.Set("Content-Type", "application/json")
		if err := request.Parse(); err != nil {
			t.Fatal(err)
		}
		response := kernel.Run(request)
		defer kernel.End(response)
		if response.GetCode() != http.StatusRequestEntityTooLarge {
			t.Fatalf("后注入且预解析的请求体绕过限制: status=%d", response.GetCode())
		}
	})

	t.Run("预解析的输入覆盖仍受限制", func(t *testing.T) {
		kernel := newLimitedDirectRunKernel(t)
		raw := httptest.NewRequest(http.MethodPost, "http://example.com/index/index/hello", nil)
		raw.Header.Set("Content-Type", "application/json")
		request := fwcontext.MustNewRequest(raw, fwcontext.WithResponseWriter(httptest.NewRecorder())).WithInput(`{"value":"` + strings.Repeat("x", 142) + `"}`)
		if err := request.Parse(); err != nil {
			t.Fatal(err)
		}
		response := kernel.Run(request)
		defer kernel.End(response)
		if response.GetCode() != http.StatusRequestEntityTooLarge {
			t.Fatalf("预解析输入覆盖绕过限制: status=%d", response.GetCode())
		}
	})
}

func newLimitedDirectRunKernel(t *testing.T) *Http {
	t.Helper()
	kernel, base := newGeneratedSkeletonHTTP(t, "", false)
	configPath := filepath.Join(base, "config", "app.json")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(content), `"allowed_hosts":["example.com"]`, `"allowed_hosts":["example.com"],"max_body_bytes":64`, 1)
	if updated == string(content) {
		t.Fatal("测试配置中未找到 Host 设置")
	}
	if err := os.WriteFile(configPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	return kernel
}
