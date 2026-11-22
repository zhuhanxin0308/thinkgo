package http

import (
	stdcontext "context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// TestSPAIndexCacheReloadAndIsolation 验证旧快照在本次缓存重新加载后仍保持原内容。
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
	if !ok || first != "first!" {
		t.Fatalf("读取首版 SPA 入口失败: %q", first)
	}
	second, ok := handler.spaIndexContent()
	if !ok || second != first {
		t.Fatalf("缓存命中应返回相同内容: %q", second)
	}

	if err := os.WriteFile(indexPath, []byte("second"), 0o600); err != nil {
		t.Fatalf("写入第二版 SPA 入口失败: %v", err)
	}
	handler.spaIndexMu.Lock()
	handler.spaIndexAt = time.Now().Add(-spaIndexCacheTTL)
	handler.spaIndexMu.Unlock()
	reloaded, ok := handler.spaIndexContent()
	if !ok || reloaded != "second" || first != "first!" {
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

func TestSPAIndexCacheHitDoesNotCopyTheDocument(t *testing.T) {
	basePath := t.TempDir()
	publicDir := filepath.Join(basePath, "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("<main>页面</main>", 4096)
	if err := os.WriteFile(filepath.Join(publicDir, "index.html"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false}))
	synctest.Test(t, func(t *testing.T) {
		if _, ok := handler.spaIndexContent(); !ok {
			t.Fatal("预热 SPA 缓存失败")
		}
		allocations := testing.AllocsPerRun(20, func() {
			if snapshot, ok := handler.spaIndexContent(); !ok || len(snapshot) != len(content) {
				t.Fatal("缓存命中内容不完整")
			}
		})
		if allocations != 0 {
			t.Fatalf("SPA 缓存命中每次分配 %.1f 次", allocations)
		}
	})
}

// TestSPAIndexNegativeCache 验证缺失入口在短暂检查窗口内不会被每个未命中请求重复访问文件系统。
func TestSPAIndexNegativeCache(t *testing.T) {
	basePath := t.TempDir()
	publicDir := filepath.Join(basePath, "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatalf("创建 public 目录失败: %v", err)
	}
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, basePath, map[string]interface{}{"enable": false}))
	synctest.Test(t, func(t *testing.T) {
		if _, ok := handler.spaIndexContent(); ok {
			t.Fatal("缺失的 SPA 入口不应被视为可用")
		}
		if err := os.WriteFile(filepath.Join(publicDir, "index.html"), []byte("created"), 0o600); err != nil {
			t.Fatalf("创建 SPA 入口失败: %v", err)
		}
		if _, ok := handler.spaIndexContent(); ok {
			t.Fatal("负缓存有效期内不应重复访问刚创建的入口")
		}
		time.Sleep(spaIndexCacheTTL)
		content, ok := handler.spaIndexContent()
		if !ok || content != "created" {
			t.Fatalf("负缓存过期后应重新读取入口，实际为 %q", content)
		}
	})
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
	synctest.Test(t, func(t *testing.T) {
		const urlPath = "/api/health"
		const content = "asset"
		open := func() (*os.File, bool) {
			file, _, ok := handler.openPublicFile(urlPath)
			if file != nil {
				t.Cleanup(func() {
					if err := file.Close(); err != nil {
						t.Errorf("关闭静态文件失败: %v", err)
					}
				})
			}
			return file, ok
		}
		if file, ok := open(); ok || file != nil {
			t.Fatal("不存在的静态前缀不得被视为可用文件")
		}
		handler.staticMissMu.RLock()
		entry, cached := handler.staticMisses["api"]
		handler.staticMissMu.RUnlock()
		if !cached || !entry.missing {
			t.Fatal("缺失静态前缀应进入短期缓存")
		}

		apiDir := filepath.Join(publicDir, "api")
		if err := os.MkdirAll(apiDir, 0o755); err != nil {
			t.Fatalf("创建静态前缀目录失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(apiDir, "health"), []byte(content), 0o600); err != nil {
			t.Fatalf("写入静态文件失败: %v", err)
		}
		time.Sleep(staticMissCacheTTL - time.Nanosecond)
		if file, ok := open(); ok || file != nil {
			t.Fatal("缓存到期前不应重新探测静态前缀")
		}
		time.Sleep(time.Nanosecond)
		file, ok := open()
		if !ok || file == nil {
			t.Fatal("缓存到期时应重新发现静态文件")
		}
		got, err := io.ReadAll(file)
		if err != nil || string(got) != content {
			t.Fatalf("缓存到期后应读取新建的静态文件: 内容=%q，错误=%v", got, err)
		}
	})
}

func TestStaticMissingChildDoesNotRenewAnExistingPrefixMiss(t *testing.T) {
	root := t.TempDir()
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, root, map[string]interface{}{"enable": false}))
	const prefix = "assets"
	const missing = "/assets/missing.js"
	const available = "/assets/ready.js"
	const content = "console.log(1)"
	if file, _, ok := handler.openPublicFile(missing); ok || file != nil {
		t.Fatal("尚未创建的目录应建立负缓存")
	}
	dir := filepath.Join(root, "public", prefix)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ready.js"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		handler.staticMissMu.Lock()
		handler.staticMisses[prefix] = staticPrefixEntry{at: time.Now().Add(-staticMissCacheTTL), missing: true}
		handler.staticMissMu.Unlock()
		miss := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.test"+missing, nil)
		if status := handler.servePublicFile(miss, req, missing); status != 0 {
			t.Fatalf("牵涉的子文件应仍然缺失: %d", status)
		}
		ready := httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "http://example.test"+available, nil)
		if status := handler.servePublicFile(ready, req, available); status != http.StatusOK ||
			ready.Body.String() != content || ready.Header().Get("Content-Type") == "" {
			t.Fatalf("首个子文件未命中后应立即访问同目录内的有效资源: %d, %q", status, ready.Body.String())
		}
	}
}

func TestStaticPrefixProbeKeepsNewerNegativeCacheDecisions(t *testing.T) {
	handler := &Http{staticMisses: make(map[string]staticPrefixEntry)}
	const prefix = "assets"
	probeAt := time.Now()
	handler.staticMisses[prefix] = staticPrefixEntry{at: probeAt.Add(-staticMissCacheTTL), missing: true}
	const workers = 12
	start := make(chan struct{})
	results := make(chan uint64, workers)
	for range workers {
		go func() {
			<-start
			probeID, _ := handler.reserveStaticPrefixProbe(prefix, probeAt)
			results <- probeID
		}()
	}
	close(start)
	var earlier uint64
	var probes int
	for range workers {
		if probeID := <-results; probeID != 0 {
			earlier = probeID
			probes++
		}
	}
	if probes != 1 {
		t.Fatalf("同一轮过期后只能有一个探测请求，实际为 %d", probes)
	}
	nextAt := probeAt.Add(staticMissCacheTTL)
	newer, cached := handler.reserveStaticPrefixProbe(prefix, nextAt)
	if cached || newer == 0 {
		t.Fatal("过期占用应允许下一轮重新探测")
	}
	handler.finishStaticPrefixProbe(prefix, newer, true, nextAt)
	handler.finishStaticPrefixProbe(prefix, earlier, false, nextAt)
	handler.finishStaticPrefixProbe(prefix, earlier, true, nextAt)
	if got := handler.staticMisses[prefix]; got.at != nextAt || !got.missing {
		t.Fatalf("旧探测不能改写新一轮负缓存: %v", got)
	}
}

func TestExpiredStaticProbeCannotRecreateClearedNegativeCache(t *testing.T) {
	handler := &Http{}
	const prefix = "assets"
	probeAt := time.Now()
	older, _ := handler.reserveStaticPrefixProbe(prefix, probeAt)
	nextAt := probeAt.Add(staticMissCacheTTL)
	newer, cached := handler.reserveStaticPrefixProbe(prefix, nextAt)
	if cached || newer == 0 {
		t.Fatal("新一轮探测应确认目录已经出现")
	}
	handler.finishStaticPrefixProbe(prefix, newer, false, nextAt)
	// 旧请求的 Lstat 结果在新探测清除负缓存后才准备发布。
	handler.finishStaticPrefixProbe(prefix, older, true, nextAt.Add(time.Millisecond))
	if _, exists := handler.staticMisses[prefix]; exists {
		t.Fatal("已经过期的 Lstat 结果不能恢复已确认失效的缓存")
	}
}

func TestSuccessfulStaticProbeClearsEarlierColdMiss(t *testing.T) {
	for _, finishOlderFirst := range []bool{true, false} {
		handler := &Http{}
		const prefix = "assets"
		now := time.Now()
		older, oldCached := handler.reserveStaticPrefixProbe(prefix, now)
		newer, newCached := handler.reserveStaticPrefixProbe(prefix, now)
		if oldCached || newCached || older == 0 || newer == 0 || newer == older {
			t.Fatal("两个冷缓存探测应独立获得编号，即使起始时间相同")
		}
		// 旧请求可能在后发请求验证文件的过程中或完成后发布 ENOENT。
		if finishOlderFirst {
			handler.finishStaticPrefixProbe(prefix, older, true, now)
		}
		handler.finishStaticPrefixProbe(prefix, newer, false, now)
		if !finishOlderFirst {
			handler.finishStaticPrefixProbe(prefix, older, true, now)
		}
		if _, exists := handler.staticMisses[prefix]; exists {
			t.Fatal("后发请求成功验证文件后，旧请求不能留下负缓存")
		}
	}
}

func TestStaticPrefixReservationsRespectCapacity(t *testing.T) {
	handler := &Http{}
	now := time.Now()
	for index := range maxStaticMissCacheEntries {
		_, _ = handler.reserveStaticPrefixProbe(strconv.Itoa(index), now)
	}
	const prefix = "extra"
	probeID, cached := handler.reserveStaticPrefixProbe(prefix, now)
	if probeID != 0 || cached || len(handler.staticMisses) != maxStaticMissCacheEntries {
		t.Fatal("缓存满时仍应允许探测文件，但不能增加缓存槽")
	}
	nextAt := now.Add(staticMissCacheTTL)
	probeID, cached = handler.reserveStaticPrefixProbe(prefix, nextAt)
	if probeID == 0 || cached || len(handler.staticMisses) != 1 {
		t.Fatal("探测过期后应回收占用")
	}
	handler.finishStaticPrefixProbe(prefix, probeID, true, nextAt.Add(staticMissCacheTTL))
	if len(handler.staticMisses) != 0 {
		t.Fatal("超时探测不能续期负缓存")
	}
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
	if err := nilHandler.Listen(); err == nil {
		t.Fatal("空 HTTP 内核运行必须返回错误")
	}

	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	handler := newTestHTTPHandler(t, app)
	if err := handler.shutdownServers(handler.newServer(), nil); err != nil {
		t.Fatalf("关闭未启动 HTTP Server 应幂等: %v", err)
	}
	http3Server := handler.newHTTP3Server(handler.newServer().Addr)
	if http3Server.MaxHeaderBytes != handler.srvConf.MaxHeaderBytes || http3Server.IdleTimeout != handler.srvConf.IdleTimeout || http3Server.TLSConfig == nil || http3Server.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("HTTP/3 必须继承请求头和空闲超时边界: maxHeader=%d idle=%s", http3Server.MaxHeaderBytes, http3Server.IdleTimeout)
	}
	if err := handler.ensureInitialized(); err != nil {
		t.Fatalf("初始化 TLS 预检测试 HTTP 内核失败: %v", err)
	}
	handler.srvConf.EnableTLS = true
	handler.srvConf.CertFile = filepath.Join(t.TempDir(), "missing-cert.pem")
	handler.srvConf.KeyFile = filepath.Join(t.TempDir(), "missing-key.pem")
	if err := handler.Listen(); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("缺失 TLS 文件应在监听前失败，实际为 %v", err)
	}

	logWriter := &serverErrorLogWriter{http: handler}
	message := []byte("server diagnostic\n")
	if written, err := logWriter.Write(message); err != nil || written != len(message) {
		t.Fatalf("Server 错误日志适配器写入失败: written=%d err=%v", written, err)
	}
}

// TestListenReturnsListenErrorAndClosesPeers 验证监听失败会退出 Listen 并执行统一关闭，而不是遗留后台服务。
func TestListenReturnsListenErrorAndClosesPeers(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用测试端口失败: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	mustHTTPConfig(t, app).Set("app.server.host", "127.0.0.1")
	mustHTTPConfig(t, app).Set("app.server.port", port)
	handler := newTestHTTPHandler(t, app)
	if err = handler.Listen(); err == nil || !strings.Contains(err.Error(), "服务异常退出") {
		t.Fatalf("端口占用应让 Listen 返回监听错误，实际为 %v", err)
	}
}

// TestRunLogsReadyOnlyAfterTCPListenerBound 验证启动日志发生在真实 TCP 监听成功之后。
func TestRunLogsReadyOnlyAfterTCPListenerBound(t *testing.T) {
	ctx, cancel := stdcontext.WithCancel(stdcontext.Background())
	defer cancel()
	config := serverConf{
		Host:            "127.0.0.1",
		Port:            0,
		ShutdownTimeout: 500 * time.Millisecond,
	}
	var dialErr error
	runErr := runHTTPServersContext(ctx, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}), config, nil, func(address string) {
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			dialErr = err
			cancel()
			return
		}
		_ = connection.Close()
		cancel()
	})
	if dialErr != nil {
		t.Fatalf("启动日志回调时 TCP 尚未可连接: %v", dialErr)
	}
	if !errors.Is(runErr, stdcontext.Canceled) {
		t.Fatalf("上下文取消应作为 Run 结果返回: %v", runErr)
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
