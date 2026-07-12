package http

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSPAIndexCacheReloadAndIsolation 验证 SPA 缓存返回副本，并在检查窗口后重新读取同尺寸内容。
func TestSPAIndexCacheReloadAndIsolation(t *testing.T) {
	basePath := t.TempDir()
	publicDir := filepath.Join(basePath, "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}
	indexPath := filepath.Join(publicDir, "index.html")
	if err := os.WriteFile(indexPath, []byte("first!"), 0o600); err != nil {
		t.Fatalf("写入首版 SPA 入口失败: %v", err)
	}
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false}))
	first, ok := handler.spaIndexContent()
	if !ok || string(first) != "first!" {
		t.Fatalf("读取首版 SPA 入口失败: %q", first)
	}
	first[0] = 'x'
	second, ok := handler.spaIndexContent()
	if !ok || string(second) != "first!" {
		t.Fatalf("调用方修改不应污染 SPA 缓存: %q", second)
	}

	if err := os.WriteFile(indexPath, []byte("second"), 0o600); err != nil {
		t.Fatalf("写入第二版 SPA 入口失败: %v", err)
	}
	handler.spaIndexMu.Lock()
	handler.spaIndexAt = time.Now().Add(-spaIndexCacheTTL)
	handler.spaIndexMu.Unlock()
	reloaded, ok := handler.spaIndexContent()
	if !ok || string(reloaded) != "second" {
		t.Fatalf("过期缓存应重新读取内容，实际为 %q", reloaded)
	}

	if err := os.WriteFile(indexPath, make([]byte, maxSPAIndexBytes+1), 0o600); err != nil {
		t.Fatalf("写入超大 SPA 入口失败: %v", err)
	}
	handler.spaIndexMu.Lock()
	handler.spaIndexAt = time.Now().Add(-spaIndexCacheTTL)
	handler.spaIndexMu.Unlock()
	if _, ok := handler.spaIndexContent(); ok {
		t.Fatal("超过上限的 SPA 入口必须被拒绝")
	}
}

// TestSPAIndexNegativeCache 验证缺失入口在短暂检查窗口内不会被每个未命中请求重复访问文件系统。
func TestSPAIndexNegativeCache(t *testing.T) {
	basePath := t.TempDir()
	publicDir := filepath.Join(basePath, "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false}))
	if _, ok := handler.spaIndexContent(); ok {
		t.Fatal("缺失的 SPA 入口不应被视为可用")
	}
	if err := os.WriteFile(filepath.Join(publicDir, "index.html"), []byte("created"), 0o600); err != nil {
		t.Fatalf("创建 SPA 入口失败: %v", err)
	}
	if _, ok := handler.spaIndexContent(); ok {
		t.Fatal("负缓存有效期内不应重复访问刚创建的入口")
	}

	handler.spaIndexMu.Lock()
	handler.spaIndexAt = time.Now().Add(-spaIndexCacheTTL)
	handler.spaIndexMu.Unlock()
	content, ok := handler.spaIndexContent()
	if !ok || string(content) != "created" {
		t.Fatalf("负缓存过期后应重新读取入口，实际为 %q", content)
	}
}

// TestStaticPathContainmentHelpers 验证相对路径判断和不存在文件安全失败。
func TestStaticPathContainmentHelpers(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "asset.txt")
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if !pathWithinRoot(root, inside) || pathWithinRoot(root, outside) {
		t.Fatal("路径根目录包含判断错误")
	}
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, root, map[string]interface{}{"enable": false}))
	if file, _, ok := handler.openPublicFile("/missing.txt"); ok || file != nil {
		t.Fatal("不存在的 public 文件必须安全失败")
	}
}

// TestStaticMissingPrefixCacheExpires 验证 API 等缺失静态前缀不会重复触发文件
// 系统探测，同时新建静态目录在短缓存窗口结束后仍可被正常访问。
func TestStaticMissingPrefixCacheExpires(t *testing.T) {
	basePath := t.TempDir()
	publicDir := filepath.Join(basePath, "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false}))
	if file, _, ok := handler.openPublicFile("/api/health"); ok || file != nil {
		t.Fatal("不存在的静态前缀不得被视为可用文件")
	}

	handler.staticMissMu.RLock()
	_, cached := handler.staticMisses["api"]
	handler.staticMissMu.RUnlock()
	if !cached {
		t.Fatal("缺失静态前缀应进入短期缓存")
	}

	apiDir := filepath.Join(publicDir, "api")
	if err := os.MkdirAll(apiDir, 0o755); err != nil {
		t.Fatalf("创建静态前缀目录失败: %v", err)
	}
	assetPath := filepath.Join(apiDir, "health")
	if err := os.WriteFile(assetPath, []byte("asset"), 0o600); err != nil {
		t.Fatalf("写入静态文件失败: %v", err)
	}
	if file, _, ok := handler.openPublicFile("/api/health"); ok || file != nil {
		t.Fatal("缓存窗口内不应重复探测刚创建的静态前缀")
	}

	handler.staticMissMu.Lock()
	handler.staticMisses["api"] = time.Now().Add(-staticMissCacheTTL)
	handler.staticMissMu.Unlock()
	file, _, ok := handler.openPublicFile("/api/health")
	if !ok || file == nil {
		t.Fatal("缓存过期后应重新发现静态文件")
	}
	_ = file.Close()
}

// TestHTTPServerHelpers 验证服务错误归一化、未启动服务关闭和 TLS 预检失败路径。
func TestHTTPServerHelpers(t *testing.T) {
	if normalizeServerError(nil) != nil || normalizeServerError(http.ErrServerClosed) != nil {
		t.Fatal("正常关闭错误应归一化为 nil")
	}
	customErr := errors.New("listen failed")
	if !errors.Is(normalizeServerError(customErr), customErr) {
		t.Fatal("真实监听错误不得被吞掉")
	}
	var nilHandler *Http
	if err := nilHandler.Run(); err == nil {
		t.Fatal("空 HTTP 内核运行必须返回错误")
	}

	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	handler := newTestHTTPHandler(t, app)
	if err := handler.shutdownServers(handler.newServer(), nil); err != nil {
		t.Fatalf("关闭未启动 HTTP Server 应幂等: %v", err)
	}
	http3Server := handler.newHTTP3Server(handler.newServer().Addr)
	if http3Server.MaxHeaderBytes != handler.srvConf.MaxHeaderBytes || http3Server.IdleTimeout != handler.srvConf.IdleTimeout {
		t.Fatalf("HTTP/3 必须继承请求头和空闲超时边界: maxHeader=%d idle=%s", http3Server.MaxHeaderBytes, http3Server.IdleTimeout)
	}
	handler.srvConf.EnableTLS = true
	handler.srvConf.CertFile = filepath.Join(t.TempDir(), "missing-cert.pem")
	handler.srvConf.KeyFile = filepath.Join(t.TempDir(), "missing-key.pem")
	if err := handler.Run(); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("缺失 TLS 文件应在监听前失败，实际为 %v", err)
	}

	logWriter := &serverErrorLogWriter{http: handler}
	message := []byte("server diagnostic\n")
	if written, err := logWriter.Write(message); err != nil || written != len(message) {
		t.Fatalf("Server 错误日志适配器写入失败: written=%d err=%v", written, err)
	}
}

// TestRunReturnsListenErrorAndClosesPeers 验证监听失败会退出 Run 并执行统一关闭，而不是遗留后台服务。
func TestRunReturnsListenErrorAndClosesPeers(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用测试端口失败: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.Config.Set("app.server.host", "127.0.0.1")
	app.Config.Set("app.server.port", port)
	handler := newTestHTTPHandler(t, app)
	if err = handler.Run(); err == nil || !strings.Contains(err.Error(), "服务异常退出") {
		t.Fatalf("端口占用应让 Run 返回监听错误，实际为 %v", err)
	}
}

// TestStatusAndHeadResponseWriters 验证状态只提交一次、Flush 隐式写 200、HEAD 丢弃实体。
func TestStatusAndHeadResponseWriters(t *testing.T) {
	recorder := httptest.NewRecorder()
	statusWriter := newStatusTrackingResponseWriter(recorder)
	statusWriter.WriteHeader(http.StatusCreated)
	statusWriter.WriteHeader(http.StatusForbidden)
	if _, err := statusWriter.Write([]byte("body")); err != nil {
		t.Fatalf("写入状态跟踪响应失败: %v", err)
	}
	statusWriter.Flush()
	if statusWriter.Status() != http.StatusCreated || !statusWriter.Written() || recorder.Code != http.StatusCreated {
		t.Fatalf("状态跟踪错误: status=%d written=%v recorder=%d", statusWriter.Status(), statusWriter.Written(), recorder.Code)
	}
	if _, _, err := statusWriter.Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("不支持 Hijack 时应返回 http.ErrNotSupported，实际为 %v", err)
	}
	if err := statusWriter.Push("/asset.js", nil); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("不支持 Push 时应返回 http.ErrNotSupported，实际为 %v", err)
	}

	headRecorder := httptest.NewRecorder()
	headWriter := &headResponseWriter{ResponseWriter: headRecorder}
	headWriter.WriteHeader(http.StatusOK)
	if written, err := headWriter.Write([]byte("discarded")); err != nil || written != len("discarded") {
		t.Fatalf("HEAD 写入器返回值错误: written=%d err=%v", written, err)
	}
	headWriter.Flush()
	if headRecorder.Body.Len() != 0 {
		t.Fatalf("HEAD 响应不得写出实体: %q", headRecorder.Body.String())
	}
}
