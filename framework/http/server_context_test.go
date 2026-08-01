package http

import (
	stdcontext "context"
	"errors"
	"testing"
	"time"
)

// nilRunContext 保留 RunContext 对 nil 上下文的兼容边界测试，避免生产调用传递 nil。
func nilRunContext() stdcontext.Context {
	return nil
}

// TestHttpRunContextRejectsNil 验证嵌入式宿主不会接受 nil 上下文。
func TestHttpRunContextRejectsNil(t *testing.T) {
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false}))
	if err := handler.RunContext(nilRunContext()); !errors.Is(err, ErrInvalidRunContext) {
		t.Fatalf("nil 上下文应返回 ErrInvalidRunContext，实际为 %v", err)
	}
}

// TestHttpRunContextStopsOnCancellation 验证真实 TCP 监听会响应上下文取消并执行优雅关闭。
func TestHttpRunContextStopsOnCancellation(t *testing.T) {
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false}))
	handler.srvConf.Host = "127.0.0.1"
	handler.srvConf.Port = 0
	handler.srvConf.ShutdownTimeout = 500 * time.Millisecond

	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 100*time.Millisecond)
	defer cancel()
	err := handler.RunContext(ctx)
	if !errors.Is(err, stdcontext.DeadlineExceeded) {
		t.Fatalf("上下文超时应返回 DeadlineExceeded，实际为 %v", err)
	}
}

// TestMultiHttpRunContextPropagatesCancellation 验证多应用宿主把同一个上下文传递给底层监听器。
func TestMultiHttpRunContextPropagatesCancellation(t *testing.T) {
	manager := newMultiHTTPTestManager(t, nil)
	host, err := NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建统一 HTTP 宿主失败: %v", err)
	}

	ctx, cancel := stdcontext.WithCancel(stdcontext.Background())
	defer cancel()
	host.runHostContext = func(received stdcontext.Context) error {
		if received != ctx {
			t.Fatalf("底层宿主未收到调用方上下文")
		}
		cancel()
		<-received.Done()
		return received.Err()
	}
	if err = host.RunContext(ctx); !errors.Is(err, stdcontext.Canceled) {
		t.Fatalf("上下文取消应从多应用生命周期返回，实际为 %v", err)
	}
}
