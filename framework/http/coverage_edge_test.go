package http

import (
	"bufio"
	"errors"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"thinkgo/framework"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/env"
	"thinkgo/framework/middleware"
	"thinkgo/framework/route"
)

func TestHTTPConfigParsesSupportedServerAndCompressionOptions(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), nil)
	app.Instance(string(framework.ServiceEnv), env.NewEnv())
	t.Setenv("SERVER_ALLOWED_HOSTS", "api.example.com, *.tenant.example.com")
	t.Setenv("SERVER_TRUSTED_PROXIES", "127.0.0.1, ::1")
	mustHTTPConfig(t, app).Set("app.server", map[string]interface{}{
		"host": "127.0.0.1", "port": 9090,
		"tls":                    map[string]interface{}{"enable": true, "cert_file": "cert.pem", "key_file": "key.pem"},
		"http3":                  true,
		"read_header_timeout_ms": 1000, "read_timeout_ms": 2000,
		"write_timeout_ms": 3000, "idle_timeout_ms": 4000, "shutdown_timeout_ms": 5000,
		"max_header_bytes": 4096, "max_body_bytes": int64(1024 * 1024), "multipart_max_memory_mb": 8,
	})
	mustHTTPConfig(t, app).Set("app.compression", map[string]interface{}{
		"enable": true, "min_size": 2048,
		"levels": map[string]interface{}{"gzip": 1, "br": 2, "deflate": 3, "zstd": 2},
	})

	server, compression, err := parseHTTPConfig(app)
	if err != nil {
		t.Fatalf("完整 HTTP 配置解析失败: %v", err)
	}
	if server.Host != "127.0.0.1" || server.Port != 9090 || !server.EnableTLS || !server.EnableHTTP3 {
		t.Fatalf("服务器配置解析结果错误: %#v", server)
	}
	if len(server.AllowedHosts) != 2 || len(server.TrustedProxies) != 2 || server.MaxHeaderBytes != 4096 {
		t.Fatalf("服务器列表或限制配置错误: %#v", server)
	}
	if !compression.Enable || compression.MinSize != 2048 || len(compression.Levels) != 4 {
		t.Fatalf("压缩配置解析结果错误: %#v", compression)
	}
}

func TestHTTPConfigRejectsInvalidShapesAndConflictingPolicies(t *testing.T) {
	if _, _, err := parseHTTPConfig(nil); !errors.Is(err, ErrInvalidHTTPConfig) {
		t.Fatalf("空应用应返回 HTTP 配置错误: %v", err)
	}
	if _, err := strictConfigMap("invalid", "app.server"); !errors.Is(err, ErrInvalidHTTPConfig) {
		t.Fatalf("非对象配置应被拒绝: %v", err)
	}
	if err := rejectUnknownConfigKeys(map[string]interface{}{"unknown": true}, "app.server", map[string]bool{}); !errors.Is(err, ErrInvalidHTTPConfig) {
		t.Fatalf("未知配置键应被拒绝: %v", err)
	}

	invalidCases := []struct {
		name   string
		server map[string]interface{}
		comp   map[string]interface{}
	}{
		{name: "http3 without tls", server: map[string]interface{}{"http3": true}},
		{name: "timeout order", server: map[string]interface{}{"read_header_timeout_ms": 2000, "read_timeout_ms": 1000}},
		{name: "empty host", server: map[string]interface{}{"host": ""}},
		{name: "conflicting compression levels", comp: map[string]interface{}{"levels": map[string]interface{}{"gzip": 1}, "level": 1}},
		{name: "invalid compression level", comp: map[string]interface{}{"levels": map[string]interface{}{"gzip": 10}}},
	}
	for _, test := range invalidCases {
		t.Run(test.name, func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), nil)
			if test.server != nil {
				mustHTTPConfig(t, app).Set("app.server", test.server)
			}
			if test.comp != nil {
				mustHTTPConfig(t, app).Set("app.compression", test.comp)
			}
			if _, _, err := parseHTTPConfig(app); !errors.Is(err, ErrInvalidHTTPConfig) {
				t.Fatalf("非法配置应返回 HTTP 配置错误: %v", err)
			}
		})
	}
}

type optionalResponseWriter struct {
	*httptest.ResponseRecorder
	hijackErr error
	pushErr   error
}

func (w *optionalResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, w.hijackErr
}

func (w *optionalResponseWriter) Push(string, *stdhttp.PushOptions) error {
	return w.pushErr
}

func TestCompressionWriterHandlesHeadFlushAndOptionalInterfaces(t *testing.T) {
	req := httptest.NewRequest(stdhttp.MethodHead, "http://example.com", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	writer := NewCompressionResponseWriter(recorder, req, -1, map[string]int{"GZIP": 1, "gzip": 9})
	if writer.encoding != "" || writer.minSize != 0 || len(writer.levels) != 0 {
		t.Fatalf("HEAD 或重复压缩配置处理错误: encoding=%q min=%d levels=%#v", writer.encoding, writer.minSize, writer.levels)
	}
	writer.WriteHeader(stdhttp.StatusCreated)
	writer.WriteHeader(stdhttp.StatusForbidden)
	if _, err := writer.Write([]byte("head-body")); err != nil {
		t.Fatalf("HEAD 响应写入失败: %v", err)
	}
	if err := writer.Close(); err != nil || recorder.Code != stdhttp.StatusCreated {
		t.Fatalf("HEAD 响应关闭或状态码错误: code=%d err=%v", recorder.Code, err)
	}

	flushRequest := httptest.NewRequest(stdhttp.MethodGet, "http://example.com", nil)
	flushRequest.Header.Set("Accept-Encoding", "gzip")
	flushRecorder := httptest.NewRecorder()
	flushWriter := NewCompressionResponseWriter(flushRecorder, flushRequest, 1, map[string]int{"gzip": 1})
	if _, err := flushWriter.Write([]byte(strings.Repeat("flush-payload-", 16))); err != nil {
		t.Fatalf("压缩流写入失败: %v", err)
	}
	flushWriter.Flush()
	if err := flushWriter.Close(); err != nil || flushRecorder.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("压缩流 Flush 未提交 gzip 响应: encoding=%q err=%v", flushRecorder.Header().Get("Content-Encoding"), err)
	}

	optionalErr := errors.New("optional interface failure")
	optional := &optionalResponseWriter{ResponseRecorder: httptest.NewRecorder(), hijackErr: optionalErr, pushErr: optionalErr}
	optionalWriter := NewCompressionResponseWriter(optional, httptest.NewRequest(stdhttp.MethodGet, "http://example.com", nil), 1, nil)
	if _, _, err := optionalWriter.Hijack(); !errors.Is(err, optionalErr) {
		t.Fatalf("应透传底层 Hijack 错误: %v", err)
	}
	if err := optionalWriter.Push("/asset.js", nil); !errors.Is(err, optionalErr) {
		t.Fatalf("应透传底层 Push 错误: %v", err)
	}
}

func TestHTTPRouteAndRequestErrorResponses(t *testing.T) {
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, t.TempDir(), nil))
	for _, test := range []struct {
		name   string
		err    error
		status int
		allow  string
	}{
		{name: "method", err: &route.MethodNotAllowedError{Allowed: []string{stdhttp.MethodGet, stdhttp.MethodHead}}, status: stdhttp.StatusMethodNotAllowed, allow: "GET, HEAD"},
		{name: "invalid path", err: route.ErrInvalidRequestPath, status: stdhttp.StatusBadRequest},
		{name: "nil request", err: route.ErrNilRequest, status: stdhttp.StatusBadRequest},
		{name: "unknown", err: errors.New("route failure"), status: stdhttp.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := handler.responseForRouteError(test.err)
			if response.GetStatus() != test.status {
				t.Fatalf("路由错误状态码错误: got=%d want=%d", response.GetStatus(), test.status)
			}
			if test.allow != "" && response.Headers().Get("Allow") != test.allow {
				t.Fatalf("Allow 响应头错误: %q", response.Headers().Get("Allow"))
			}
		})
	}

	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "too large", err: fwcontext.ErrRequestBodyTooLarge, status: stdhttp.StatusRequestEntityTooLarge},
		{name: "malformed", err: errors.New("bad request"), status: stdhttp.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.writeRequestParseError(recorder, test.err)
			if recorder.Code != test.status {
				t.Fatalf("请求解析错误状态码错误: got=%d want=%d", recorder.Code, test.status)
			}
		})
	}
}

func TestHTTPHelpersHandleLoggingAndPanicBoundaries(t *testing.T) {
	manager := newMultiHTTPTestManager(t, nil)
	host, err := NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建统一 HTTP 宿主失败: %v", err)
	}
	host.logHTTPError("测试错误", errors.New("failure"))
	host.logServerStarted("127.0.0.1:8080")
	var nilHost *MultiHttp
	nilHost.logHTTPError("ignored", errors.New("ignored"))
	nilHost.logServerStarted("ignored")

	handler := newTestHTTPHandler(t, newTestHTTPApp(t, t.TempDir(), nil))
	terminatorCalled := false
	handler.runTerminators(nil, fwcontext.NewResponse(), []middleware.Terminator{
		nil,
		func(*fwcontext.Request, *fwcontext.Response) { terminatorCalled = true },
		func(*fwcontext.Request, *fwcontext.Response) { panic("terminator panic") },
	})
	if !terminatorCalled {
		t.Fatal("有效 terminate 回调未执行")
	}

	panicWriter := &CompressionResponseWriter{pooledWriter: &pooledCompressionWriter{inUse: true}}
	if err := safeCloseCompressionWriter(panicWriter); err == nil {
		t.Fatal("压缩写入器 panic 应转换为错误")
	}
}

func TestServerErrorLogWriterUsesCallbackWithoutHTTP(t *testing.T) {
	var got string
	writer := &serverErrorLogWriter{onError: func(message string, err error) {
		got = message + ":" + err.Error()
	}}
	message := []byte("server failure\n")
	if written, err := writer.Write(message); err != nil || written != len(message) {
		t.Fatalf("服务器错误日志写入失败: written=%d err=%v", written, err)
	}
	if !strings.Contains(got, "server failure") {
		t.Fatalf("服务器错误日志回调未收到内容: %q", got)
	}
}

func TestHTTPConfigDurationRejectsZeroAndTooLargeValues(t *testing.T) {
	for _, value := range []interface{}{0, int64(10*time.Minute.Milliseconds() + 1)} {
		if _, err := configDuration(map[string]interface{}{"timeout": value}, "timeout", time.Second); err == nil {
			t.Fatalf("超出范围的超时值 %v 应被拒绝", value)
		}
	}
}
