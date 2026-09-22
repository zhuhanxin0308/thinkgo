package http

import (
	stdcontext "context"
	"errors"
	"testing"
	"time"
)

// nilRunContext 保留 ListenContext 对 nil 上下文的兼容边界测试，避免生产调用传递 nil。
func nilRunContext() stdcontext.Context {
	return nil
}

// TestHttpListenContextRejectsNil 验证嵌入式宿主不会接受 nil 上下文。
func TestHttpListenContextRejectsNil(t *testing.T) {
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false}))
	if err := handler.ListenContext(nilRunContext()); !errors.Is(err, ErrInvalidRunContext) {
		t.Fatalf("nil 上下文应返回 ErrInvalidRunContext，实际为 %v", err)
	}
}

// TestHttpListenContextStopsOnCancellation 验证真实 TCP 监听会响应上下文取消并执行优雅关闭。
func TestHttpListenContextStopsOnCancellation(t *testing.T) {
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false}))
	if err := handler.ensureInitialized(); err != nil {
		t.Fatalf("初始化上下文取消测试 HTTP 内核失败: %v", err)
	}
	handler.srvConf.Host = "127.0.0.1"
	handler.srvConf.Port = 0
	handler.srvConf.ShutdownTimeout = 500 * time.Millisecond

	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 100*time.Millisecond)
	defer cancel()
	err := handler.ListenContext(ctx)
	if !errors.Is(err, stdcontext.DeadlineExceeded) {
		t.Fatalf("上下文超时应返回 DeadlineExceeded，实际为 %v", err)
	}
}
