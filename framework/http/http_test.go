package http

import (
	"bytes"
	"compress/gzip"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"thinkgo/framework"
	"thinkgo/framework/config"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/log"
	"thinkgo/framework/middleware"
	"thinkgo/framework/route"
)

// newTestHTTPApp 创建最小化测试应用，避免在单测中引入真实数据库和外部依赖。
func newTestHTTPApp(t *testing.T, basePath string, compression map[string]interface{}) *framework.App {
	t.Helper()

	cfg := config.NewConfig()
	cfg.Set("app.server", map[string]interface{}{
		"host":                    "127.0.0.1",
		"port":                    8080,
		"read_header_timeout_ms":  1000,
		"read_timeout_ms":         2000,
		"write_timeout_ms":        2000,
		"idle_timeout_ms":         3000,
		"shutdown_timeout_ms":     1000,
		"max_header_bytes":        1024 * 1024,
		"max_body_bytes":          1024 * 1024,
		"multipart_max_memory_mb": 8,
	})
	cfg.Set("app.compression", compression)

	app := &framework.App{
		BasePath:   basePath,
		Config:     cfg,
		Route:      route.NewRouter(),
		Middleware: middleware.NewPipeline(),
		Log:        log.NewLog(),
	}

	t.Cleanup(app.Log.Shutdown)
	return app
}

// TestServeHTTPPreventsStaticTraversal 验证静态文件处理不会越权访问 public 目录之外的文件。
func TestServeHTTPPreventsStaticTraversal(t *testing.T) {
	basePath := t.TempDir()
	publicDir := filepath.Join(basePath, "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}

	secretPath := filepath.Join(basePath, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("top-secret"), 0o600); err != nil {
		t.Fatalf("写入敏感文件失败: %v", err)
	}

	app := newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false})
	handler := NewHttp(app)

	req := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/", nil)
	req.URL.Path = "/..\\secret.txt"
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != stdhttp.StatusNotFound {
		t.Fatalf("目录穿越请求应返回 404，实际为 %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "top-secret") {
		t.Fatal("目录穿越请求不应读到 public 目录之外的内容")
	}
}

// TestCompressionSkipsSmallResponse 验证小于压缩阈值的响应不会被无意义压缩。
func TestCompressionSkipsSmallResponse(t *testing.T) {
	basePath := t.TempDir()
	publicDir := filepath.Join(basePath, "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}

	app := newTestHTTPApp(t, basePath, map[string]interface{}{
		"enable":   true,
		"min_size": 16,
		"levels": map[string]interface{}{
			"gzip": 1,
		},
	})
	app.Route.Get("/tiny", func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("tiny")
	})

	handler := NewHttp(app)
	req := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/tiny", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if got := recorder.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("小响应不应被压缩，实际 Content-Encoding=%q", got)
	}
	if body := recorder.Body.String(); body != "tiny" {
		t.Fatalf("小响应应保持原始内容，实际为 %q", body)
	}
}

// TestCompressionCompressesLargeResponse 验证达到阈值的响应仍会按协商算法压缩。
func TestCompressionCompressesLargeResponse(t *testing.T) {
	basePath := t.TempDir()
	publicDir := filepath.Join(basePath, "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}

	largeBody := strings.Repeat("payload-", 8)
	app := newTestHTTPApp(t, basePath, map[string]interface{}{
		"enable":   true,
		"min_size": 16,
		"levels": map[string]interface{}{
			"gzip": 1,
		},
	})
	app.Route.Get("/large", func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content(largeBody)
	})

	handler := NewHttp(app)
	req := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/large", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if got := recorder.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("大响应应被 gzip 压缩，实际 Content-Encoding=%q", got)
	}

	reader, err := gzip.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatalf("创建 gzip 解压器失败: %v", err)
	}
	defer reader.Close()

	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("读取解压内容失败: %v", err)
	}
	if string(body) != largeBody {
		t.Fatalf("解压后的响应内容不正确，期望 %q，实际 %q", largeBody, string(body))
	}
}

// TestNewServerUsesConfiguredTimeouts 验证 HTTP 内核会把配置映射为显式的服务端超时边界。
func TestNewServerUsesConfiguredTimeouts(t *testing.T) {
	basePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(basePath, "public"), 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}

	app := newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false})
	app.Config.Set("app.server", map[string]interface{}{
		"host":                    "127.0.0.1",
		"port":                    18080,
		"read_header_timeout_ms":  1500,
		"read_timeout_ms":         2500,
		"write_timeout_ms":        3500,
		"idle_timeout_ms":         4500,
		"shutdown_timeout_ms":     5500,
		"max_header_bytes":        2048,
		"max_body_bytes":          4096,
		"multipart_max_memory_mb": 4,
	})

	handler := NewHttp(app)
	server := handler.newServer()

	if server.Addr != "127.0.0.1:18080" {
		t.Fatalf("服务监听地址不正确，实际为 %q", server.Addr)
	}
	if server.ReadHeaderTimeout.Milliseconds() != 1500 {
		t.Fatalf("ReadHeaderTimeout 不正确，实际为 %dms", server.ReadHeaderTimeout.Milliseconds())
	}
	if server.ReadTimeout.Milliseconds() != 2500 {
		t.Fatalf("ReadTimeout 不正确，实际为 %dms", server.ReadTimeout.Milliseconds())
	}
	if server.WriteTimeout.Milliseconds() != 3500 {
		t.Fatalf("WriteTimeout 不正确，实际为 %dms", server.WriteTimeout.Milliseconds())
	}
	if server.IdleTimeout.Milliseconds() != 4500 {
		t.Fatalf("IdleTimeout 不正确，实际为 %dms", server.IdleTimeout.Milliseconds())
	}
	if server.MaxHeaderBytes != 2048 {
		t.Fatalf("MaxHeaderBytes 不正确，实际为 %d", server.MaxHeaderBytes)
	}
}

// TestServeHTTPRejectsOversizedRequestBody 验证超过上限的请求体会在入口被直接拒绝。
func TestServeHTTPRejectsOversizedRequestBody(t *testing.T) {
	basePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(basePath, "public"), 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}

	app := newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false})
	app.Config.Set("app.server.max_body_bytes", 8)
	app.Route.Post("/echo", func(req *fwcontext.Request) *fwcontext.Response {
		body, err := req.Body()
		if err != nil {
			return fwcontext.NewResponse().Code(stdhttp.StatusBadRequest).Content(err.Error())
		}
		return fwcontext.NewResponse().Content(string(body))
	})

	handler := NewHttp(app)
	req := httptest.NewRequest(stdhttp.MethodPost, "http://example.com/echo", strings.NewReader("0123456789"))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != stdhttp.StatusRequestEntityTooLarge {
		t.Fatalf("超过限制的请求体应返回 413，实际为 %d", recorder.Code)
	}
}
