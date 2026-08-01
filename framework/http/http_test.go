package http

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"thinkgo/framework"
	"thinkgo/framework/config"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/log"
	"thinkgo/framework/metrics"
	"thinkgo/framework/middleware"
	"thinkgo/framework/route"
)

// newTestHTTPApp 创建最小化测试应用，避免在单测中引入真实数据库和外部依赖。
func newTestHTTPApp(t testing.TB, basePath string, compression map[string]interface{}) *framework.App {
	t.Helper()
	ensureHTTPTestConfigFiles(t, basePath)

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

	app, err := framework.BuildConsoleApp(basePath)
	if err != nil {
		t.Fatalf("构建 HTTP 测试应用失败: %v", err)
	}
	app.Instance(string(framework.ServiceConfig), cfg)
	t.Cleanup(func() { _ = app.Close() })
	return app
}

// ensureHTTPTestConfigFiles 为 HTTP 测试补齐严格构造所需的基础配置。
func ensureHTTPTestConfigFiles(t testing.TB, basePath string) {
	t.Helper()
	configPath := filepath.Join(basePath, "config")
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatalf("创建 HTTP 测试配置目录失败: %v", err)
	}
	configs := map[string]string{
		"app.json":     `{"app_env":"test","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`,
		"log.json":     `{"default":"file","channels":{"file":{"type":"file","path":"runtime/log"}}}`,
		"cache.json":   `{"default":"file","stores":{"file":{"type":"file","path":"runtime/cache"}}}`,
		"view.json":    `{"view_path":"app/view","view_suffix":"html","cache":false}`,
		"cookie.json":  `{}`,
		"session.json": `{"type":"memory","name":"TESTSESSID","expire":600}`,
	}
	for name, content := range configs {
		path := filepath.Join(configPath, name)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			t.Fatalf("检查 HTTP 测试配置 %q 失败: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("写入 HTTP 测试配置 %q 失败: %v", name, err)
		}
	}
}

// mustHTTPConfig 通过服务解析边界取得测试配置。
func mustHTTPConfig(t testing.TB, app *framework.App) *config.Config {
	t.Helper()
	configuration, err := framework.ResolveServiceAs[*config.Config](app, framework.ServiceConfig)
	if err != nil {
		t.Fatalf("解析 HTTP 测试配置失败: %v", err)
	}
	return configuration
}

// mustHTTPRoute 通过服务解析边界取得测试路由器。
func mustHTTPRoute(t testing.TB, app *framework.App) *route.Router {
	t.Helper()
	router, err := framework.ResolveServiceAs[*route.Router](app, framework.ServiceRoute)
	if err != nil {
		t.Fatalf("解析 HTTP 测试路由失败: %v", err)
	}
	return router
}

// mustHTTPMiddleware 通过服务解析边界取得测试中间件管线。
func mustHTTPMiddleware(t testing.TB, app *framework.App) *middleware.Pipeline {
	t.Helper()
	pipeline, err := framework.ResolveServiceAs[*middleware.Pipeline](app, framework.ServiceMiddleware)
	if err != nil {
		t.Fatalf("解析 HTTP 测试中间件失败: %v", err)
	}
	return pipeline
}

// mustHTTPMetrics 通过服务解析边界取得测试指标注册表。
func mustHTTPMetrics(t testing.TB, app *framework.App) *metrics.Registry {
	t.Helper()
	registry, err := framework.ResolveServiceAs[*metrics.Registry](app, framework.ServiceMetrics)
	if err != nil {
		t.Fatalf("解析 HTTP 测试指标失败: %v", err)
	}
	return registry
}

// mustHTTPLog 通过服务解析边界取得测试日志服务。
func mustHTTPLog(t testing.TB, app *framework.App) *log.Log {
	t.Helper()
	logger, err := framework.ResolveServiceAs[*log.Log](app, framework.ServiceLog)
	if err != nil {
		t.Fatalf("解析 HTTP 测试日志失败: %v", err)
	}
	return logger
}

type accessLogProbeDriver struct {
	writes atomic.Int32
}

func (d *accessLogProbeDriver) SaveEntries([]*log.LogEntry) error { return nil }

func (d *accessLogProbeDriver) WriteEntry(*log.LogEntry) error {
	d.writes.Add(1)
	return nil
}

func (d *accessLogProbeDriver) Close() error { return nil }

// TestWriteAccessLogFiltersBeforeRequestFields 验证 Info 禁用时访问日志不读取请求对象或构造字段。
func TestWriteAccessLogFiltersBeforeRequestFields(t *testing.T) {
	driver := &accessLogProbeDriver{}
	logger := log.NewLog(driver)
	logger.SetLevels([]string{log.LevelError})
	h := &Http{log: logger}
	h.writeAccessLog(nil)
	if writes := driver.writes.Load(); writes != 0 {
		t.Fatalf("Info 禁用时不应写访问日志，实际写入 %d 次", writes)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭访问日志测试器失败: %v", err)
	}
}

func newTestHTTPHandler(t *testing.T, app *framework.App) *Http {
	t.Helper()
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建 HTTP 内核失败: %v", err)
	}
	return handler
}

// TestServeHTTPRecordsOptInMetrics 验证指标默认不介入请求，并在显式启用后记录完整响应状态与耗时。
func TestServeHTTPRecordsOptInMetrics(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	registry := mustHTTPMetrics(t, app)
	registry.Enable()
	router := mustHTTPRoute(t, app)
	if _, err := router.Get("/metrics-probe", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	}); err != nil {
		t.Fatalf("注册指标探针路由失败: %v", err)
	}
	handler := newTestHTTPHandler(t, app)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/metrics-probe", nil))
	if recorder.Code != stdhttp.StatusOK || recorder.Body.String() != "ok" {
		t.Fatalf("指标探针请求失败: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	snapshot := registry.Snapshot()
	if snapshot.Requests != 1 || snapshot.StatusClasses[1] != 1 || snapshot.DurationCount != 1 {
		t.Fatalf("HTTP 指标未记录完整请求: %#v", snapshot)
	}
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
	handler := newTestHTTPHandler(t, app)

	req := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/", nil)
	req.URL.Path = "/..\\secret.txt"
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != stdhttp.StatusBadRequest {
		t.Fatalf("目录穿越请求应返回 400，实际为 %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "top-secret") {
		t.Fatal("目录穿越请求不应读到 public 目录之外的内容")
	}
}

// TestServeHTTPRunsMiddlewareBeforeStaticFile 验证静态文件不能绕过全局认证中间件。
func TestServeHTTPRunsMiddlewareBeforeStaticFile(t *testing.T) {
	basePath := t.TempDir()
	publicDir := filepath.Join(basePath, "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "private.txt"), []byte("private"), 0o600); err != nil {
		t.Fatalf("写入静态文件失败: %v", err)
	}

	app := newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false})
	mustHTTPMiddleware(t, app).Pipe(func(_ *fwcontext.Request, _ func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		return fwcontext.NewResponse().Code(stdhttp.StatusUnauthorized).Content("blocked")
	})
	handler := newTestHTTPHandler(t, app)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/private.txt", nil))

	if recorder.Code != stdhttp.StatusUnauthorized || recorder.Body.String() != "blocked" {
		t.Fatalf("静态文件不应绕过全局中间件: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

// TestServeHTTPRejectsEncodedStaticSeparator 验证编码斜杠不能改变静态文件目录层级。
func TestServeHTTPRejectsEncodedStaticSeparator(t *testing.T) {
	basePath := t.TempDir()
	nestedDir := filepath.Join(basePath, "public", "assets")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("创建静态目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "private.txt"), []byte("private-static"), 0o600); err != nil {
		t.Fatalf("写入静态文件失败: %v", err)
	}
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/assets%2Fprivate.txt", nil))
	if recorder.Code != stdhttp.StatusNotFound || strings.Contains(recorder.Body.String(), "private-static") {
		t.Fatalf("编码路径分隔符不得命中静态文件，status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

// TestServeHTTPRejectsUnlistedHost 验证配置主机白名单后，伪造 Host 会在入口被拒绝。
func TestServeHTTPRejectsUnlistedHost(t *testing.T) {
	basePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(basePath, "public"), 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}

	app := newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false})
	mustHTTPConfig(t, app).Set("app.server.allowed_hosts", []interface{}{"app.example.com"})
	mustHTTPRoute(t, app).Get("/ok", func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	})

	handler := newTestHTTPHandler(t, app)
	req := httptest.NewRequest(stdhttp.MethodGet, "http://evil.example.com/ok", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != stdhttp.StatusMisdirectedRequest {
		t.Fatalf("未列入白名单的 Host 应返回 421，实际为 %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "ok") {
		t.Fatalf("未列入白名单的 Host 不应进入业务路由，响应为 %s", recorder.Body.String())
	}
}

// TestServeHTTPAllowsConfiguredHost 验证 Host 白名单允许合法主机继续进入业务路由。
func TestServeHTTPAllowsConfiguredHost(t *testing.T) {
	basePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(basePath, "public"), 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}

	app := newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false})
	mustHTTPConfig(t, app).Set("app.server.allowed_hosts", []interface{}{"app.example.com"})
	mustHTTPRoute(t, app).Get("/ok", func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	})

	handler := newTestHTTPHandler(t, app)
	req := httptest.NewRequest(stdhttp.MethodGet, "http://app.example.com:8080/ok", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != stdhttp.StatusOK {
		t.Fatalf("白名单内 Host 应正常响应，实际为 %d", recorder.Code)
	}
	if recorder.Body.String() != "ok" {
		t.Fatalf("白名单内 Host 应进入业务路由，实际响应为 %s", recorder.Body.String())
	}
}

// TestServeHTTPRejectsMalformedHostWithoutWhitelist 验证空白名单只放开合法 Host，不放开用户信息或控制字符语法。
func TestServeHTTPRejectsMalformedHostWithoutWhitelist(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	handler := newTestHTTPHandler(t, app)
	req := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/", nil)
	req.Host = "trusted.example@evil.example"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != stdhttp.StatusMisdirectedRequest {
		t.Fatalf("非法 Host 即使未配置白名单也应返回 421，实际为 %d", recorder.Code)
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
	mustHTTPRoute(t, app).Get("/tiny", func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("tiny")
	})

	handler := newTestHTTPHandler(t, app)
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
	mustHTTPRoute(t, app).Get("/large", func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content(largeBody)
	})

	handler := newTestHTTPHandler(t, app)
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
	mustHTTPConfig(t, app).Set("app.server", map[string]interface{}{
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

	handler := newTestHTTPHandler(t, app)
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
	mustHTTPConfig(t, app).Set("app.server.max_body_bytes", 8)
	mustHTTPRoute(t, app).Post("/echo", func(req *fwcontext.Request) *fwcontext.Response {
		body, err := req.Body()
		if err != nil {
			return fwcontext.NewResponse().Code(stdhttp.StatusBadRequest).Content(err.Error())
		}
		return fwcontext.NewResponse().Content(string(body))
	})

	handler := newTestHTTPHandler(t, app)
	req := httptest.NewRequest(stdhttp.MethodPost, "http://example.com/echo", strings.NewReader("0123456789"))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != stdhttp.StatusRequestEntityTooLarge {
		t.Fatalf("超过限制的请求体应返回 413，实际为 %d", recorder.Code)
	}
}

// TestServeHTTPRejectsChunkedOversizedRequestBody 验证未知长度/chunked 请求体超限后不会被业务当作空 body 处理。
func TestServeHTTPRejectsChunkedOversizedRequestBody(t *testing.T) {
	basePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(basePath, "public"), 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}

	app := newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false})
	mustHTTPConfig(t, app).Set("app.server.max_body_bytes", 8)
	mustHTTPRoute(t, app).Post("/profile", func(req *fwcontext.Request) *fwcontext.Response {
		name := req.Post("name", "empty")
		return fwcontext.NewResponse().Content("name=" + name)
	})

	handler := newTestHTTPHandler(t, app)
	req := httptest.NewRequest(stdhttp.MethodPost, "http://example.com/profile?name=query", strings.NewReader(`{"name":"0123456789"}`))
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != stdhttp.StatusRequestEntityTooLarge {
		t.Fatalf("chunked 超限请求体应返回 413，实际为 %d，body=%q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "query") {
		t.Fatalf("body 读取失败后不应回落到 query 参数，实际响应为 %q", recorder.Body.String())
	}
}

// TestServeHTTPRejectsStaticSymlinkEscape 验证 public 内符号链接不能跳转到根目录之外读取文件。
func TestServeHTTPRejectsStaticSymlinkEscape(t *testing.T) {
	basePath := t.TempDir()
	publicDir := filepath.Join(basePath, "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}
	secretPath := filepath.Join(basePath, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("symlink-secret"), 0o600); err != nil {
		t.Fatalf("写入敏感文件失败: %v", err)
	}
	linkPath := filepath.Join(publicDir, "linked.txt")
	if err := os.Symlink(secretPath, linkPath); err != nil {
		t.Skipf("当前环境不允许创建符号链接: %v", err)
	}

	handler := newTestHTTPHandler(t, newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/linked.txt", nil))
	if recorder.Code != stdhttp.StatusNotFound || strings.Contains(recorder.Body.String(), "symlink-secret") {
		t.Fatalf("越界符号链接必须返回 404，status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

// TestServeHTTPDoesNotUseSPAFallbackForWriteMethods 验证写请求未命中路由时不会伪装成 SPA 页面成功响应。
func TestServeHTTPDoesNotUseSPAFallbackForWriteMethods(t *testing.T) {
	basePath := t.TempDir()
	publicDir := filepath.Join(basePath, "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "index.html"), []byte("spa-index"), 0o600); err != nil {
		t.Fatalf("写入 SPA 入口失败: %v", err)
	}

	handler := newTestHTTPHandler(t, newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodPost, "http://example.com/missing", nil))
	if recorder.Code != stdhttp.StatusNotFound || strings.Contains(recorder.Body.String(), "spa-index") {
		t.Fatalf("POST 未命中应返回 404，status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

// TestServeHTTPRejectsMalformedJSONBeforeBusiness 验证结构化请求体错误在进入中间件和业务前被阻断。
func TestServeHTTPRejectsMalformedJSONBeforeBusiness(t *testing.T) {
	basePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(basePath, "public"), 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}
	app := newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false})
	var calls atomic.Int32
	if _, err := mustHTTPRoute(t, app).Post("/users", func(req *fwcontext.Request) *fwcontext.Response {
		calls.Add(1)
		return fwcontext.NewResponse().Content("created")
	}); err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}
	handler := newTestHTTPHandler(t, app)
	req := httptest.NewRequest(stdhttp.MethodPost, "http://example.com/users", strings.NewReader(`{"role":"user","role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != stdhttp.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("非法 JSON 必须在业务前返回 400，status=%d calls=%d", recorder.Code, calls.Load())
	}
}

// TestServeHTTPReturnsMethodNotAllowed 验证路径存在但方法不匹配时返回 405 和稳定 Allow 头。
func TestServeHTTPReturnsMethodNotAllowed(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if _, err := mustHTTPRoute(t, app).Get("/items", "Item@Index"); err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}
	handler := newTestHTTPHandler(t, app)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodPost, "http://example.com/items", nil))
	if recorder.Code != stdhttp.StatusMethodNotAllowed {
		t.Fatalf("方法不匹配应返回 405，实际为 %d", recorder.Code)
	}
	if recorder.Header().Get("Allow") != "GET, HEAD, OPTIONS" {
		t.Fatalf("Allow 头错误: %q", recorder.Header().Get("Allow"))
	}
}

// TestNewHttpRejectsUnsafeConfiguration 验证服务安全边界配置不会被截断或静默回退。
func TestNewHttpRejectsUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*framework.App)
	}{
		{name: "小数端口", mutate: func(app *framework.App) { mustHTTPConfig(t, app).Set("app.server.port", 8080.5) }},
		{name: "非法代理网段", mutate: func(app *framework.App) {
			mustHTTPConfig(t, app).Set("app.server.trusted_proxies", []interface{}{"127.0.0.1/33"})
		}},
		{name: "HTTP3 未启用 TLS", mutate: func(app *framework.App) { mustHTTPConfig(t, app).Set("app.server.http3", true) }},
		{name: "零请求体上限", mutate: func(app *framework.App) { mustHTTPConfig(t, app).Set("app.server.max_body_bytes", 0) }},
		{name: "读取超时短于请求头超时", mutate: func(app *framework.App) {
			mustHTTPConfig(t, app).Set("app.server.read_header_timeout_ms", 2000)
			mustHTTPConfig(t, app).Set("app.server.read_timeout_ms", 1000)
		}},
		{name: "未知配置键", mutate: func(app *framework.App) { mustHTTPConfig(t, app).Set("app.server.max_boby_bytes", 100) }},
		{name: "非法 gzip 等级", mutate: func(app *framework.App) {
			mustHTTPConfig(t, app).Set("app.compression", map[string]interface{}{"enable": true, "levels": map[string]interface{}{"gzip": 99}})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
			test.mutate(app)
			if _, err := NewHttp(app); !errors.Is(err, ErrInvalidHTTPConfig) {
				t.Fatalf("应返回 ErrInvalidHTTPConfig，实际为 %v", err)
			}
		})
	}
}

// TestHttpUsesCompiledTrustedProxySet 验证 HTTP 内核使用启动期编译的代理集合，而不是在请求热路径重新解析配置。
func TestHttpUsesCompiledTrustedProxySet(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	mustHTTPConfig(t, app).Set("app.server.trusted_proxies", []interface{}{"127.0.0.1/32"})
	observedSecure := false
	mustHTTPRoute(t, app).Get("/proxy-secure", func(req *fwcontext.Request) *fwcontext.Response {
		observedSecure = req.IsSsl()
		return fwcontext.NewResponse().Content("ok")
	})

	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建 HTTP 内核失败: %v", err)
	}
	if handler.srvConf.TrustedProxySet == nil {
		t.Fatal("启动期应创建预编译的受信代理集合")
	}

	raw := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/proxy-secure", nil)
	raw.RemoteAddr = "127.0.0.1:4321"
	raw.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, raw)
	if recorder.Code != stdhttp.StatusOK || recorder.Body.String() != "ok" || !observedSecure {
		t.Fatalf("HTTP 请求未使用预编译代理策略: status=%d body=%q secure=%t", recorder.Code, recorder.Body.String(), observedSecure)
	}
}
