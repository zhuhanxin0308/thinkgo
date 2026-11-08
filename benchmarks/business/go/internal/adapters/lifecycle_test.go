package adapters_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	beegoadapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/beego"
	echoadapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/echo"
	fiberadapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/fiber"
	ginadapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/gin"
	irisadapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/iris"
	thinkgoadapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/thinkgo"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/serve"
)

// TestNativeLifecycles 验证真实监听就绪和取消排空，不把httptest结果冒充网络吞吐。
func TestNativeLifecycles(t *testing.T) {
	builders := map[string]func(*probeService, string) (serve.Application, error){
		"thinkgo": func(s *probeService, a string) (serve.Application, error) { return thinkgoadapter.New(s, a) },
		"gin":     func(s *probeService, a string) (serve.Application, error) { return ginadapter.New(s, a) },
		"echo":    func(s *probeService, a string) (serve.Application, error) { return echoadapter.New(s, a) },
		"fiber":   func(s *probeService, a string) (serve.Application, error) { return fiberadapter.New(s, a) },
		"beego":   func(s *probeService, a string) (serve.Application, error) { return beegoadapter.New(s, a) },
		"iris":    func(s *probeService, a string) (serve.Application, error) { return irisadapter.New(s, a) },
	}
	const startupPollInterval = 10 * time.Millisecond
	const lifecycleDeadline = 3 * time.Second
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			reservation, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := reservation.Addr().String()
			if err := reservation.Close(); err != nil {
				t.Fatal(err)
			}
			app, err := build(&probeService{}, address)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- app.Run(ctx) }()
			client := &http.Client{Timeout: time.Second}
			defer client.CloseIdleConnections()
			deadline := time.NewTimer(lifecycleDeadline)
			defer deadline.Stop()
			ticker := time.NewTicker(startupPollInterval)
			defer ticker.Stop()
			ready := false
			for !ready {
				select {
				case err := <-result:
					_ = app.Close()
					t.Fatalf("监听就绪前退出: %v", err)
				case <-deadline.C:
					cancel()
					<-result
					_ = app.Close()
					t.Fatal("监听未按预算就绪")
				case <-ticker.C:
					response, err := client.Get("http://" + address + "/healthz")
					if err == nil {
						ready = response.StatusCode == http.StatusOK
						response.Body.Close()
					}
				}
			}
			large, err := http.NewRequest(http.MethodPost, "http://"+address+"/api/orders", strings.NewReader(`{"padding":"`+strings.Repeat("x", 300)+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			large.Header.Set("Authorization", "Bearer valid")
			large.Header.Set("Content-Type", "application/json")
			response, err := client.Do(large)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusRequestEntityTooLarge && response.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("真实HTTP正文上限失败: %d", response.StatusCode)
			}
			client.CloseIdleConnections()
			cancel()
			select {
			case err := <-result:
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Error(err)
				}
			case <-time.After(lifecycleDeadline):
				t.Fatal("请求排空与监听关闭未完成")
			}
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
