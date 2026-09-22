package otlp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/telemetry"
)

func telemetryProviderTestApp(t *testing.T) (*framework.App, *config.Config) {
	t.Helper()
	app := framework.NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	configuration, err := framework.ResolveServiceAs[*config.Config](app, framework.ServiceConfig)
	if err != nil {
		t.Fatalf("解析遥测测试配置失败: %v", err)
	}
	return app, configuration
}

// TestProviderExportsOTLPTraceAndShutsDown 验证配置驱动的 SDK 会导出 HTTP Span，并在关闭时刷新批处理队列。
func TestProviderExportsOTLPTraceAndShutsDown(t *testing.T) {
	var requests atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path != "/v1/traces" || request.Header.Get("X-Tenant") != "tenant-a" {
			http.Error(writer, "invalid export request", http.StatusBadRequest)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	app, configuration := telemetryProviderTestApp(t)
	configuration.Set("telemetry", map[string]interface{}{
		"enable":                true,
		"service_name":          "orders-api",
		"service_version":       "2.0.0",
		"environment":           "test",
		"endpoint":              collector.URL + "/v1/traces",
		"headers":               map[string]interface{}{"X-Tenant": "tenant-a"},
		"timeout_ms":            2000,
		"sample_ratio":          1.0,
		"batch_timeout_ms":      50,
		"max_export_batch_size": 32,
		"max_queue_size":        64,
	})
	provider := NewProvider()
	if err := provider.Initialize(app); err != nil {
		t.Fatalf("初始化 OTLP Provider 失败: %v", err)
	}
	tracing, err := framework.ResolveServiceAs[*telemetry.Tracing](app, framework.ServiceTelemetry)
	if err != nil || !tracing.Enabled() {
		t.Fatalf("OTLP Provider 未绑定启用的追踪服务: tracing=%v err=%v", tracing, err)
	}
	ctx, span := tracing.StartServer(context.Background(), http.Header{}, "GET", "/orders/42", "api.example.com")
	if span == nil {
		t.Fatal("OTLP Provider 启用后必须创建 Span")
	}
	telemetry.SetHTTPRoute(ctx, "/orders/:id")
	telemetry.EndHTTPServer(ctx, http.StatusOK, nil)
	if err := provider.Shutdown(app); err != nil {
		t.Fatalf("关闭 OTLP Provider 失败: %v", err)
	}
	if requests.Load() == 0 {
		t.Fatal("关闭 OTLP Provider 时必须刷新并导出 Span")
	}
}

// TestProviderRejectsUnsafeAndUnknownConfiguration 验证未知字段、带凭据 URL 和越界采样率都会阻止启动。
func TestProviderRejectsUnsafeAndUnknownConfiguration(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		values map[string]interface{}
	}{
		{name: "unknown", values: map[string]interface{}{"enable": true, "unknown": true}},
		{name: "credential_url", values: map[string]interface{}{"enable": true, "endpoint": "https://user:pass@example.com/v1/traces"}},
		{name: "sample_ratio", values: map[string]interface{}{"enable": true, "endpoint": "https://example.com/v1/traces", "sample_ratio": 2.0}},
		{name: "duplicate_header", values: map[string]interface{}{"enable": true, "endpoint": "https://example.com/v1/traces", "headers": `{"Authorization":"first","Authorization":"second"}`}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			app, configuration := telemetryProviderTestApp(t)
			configuration.Set("telemetry", testCase.values)
			if err := NewProvider().Initialize(app); err == nil {
				t.Fatal("非法 OTLP 配置必须阻止 Provider 初始化")
			}
		})
	}
}

// TestProviderDisabledKeepsNoopTracing 验证默认关闭不会创建 exporter 或改变低开销路径。
func TestProviderDisabledKeepsNoopTracing(t *testing.T) {
	app, configuration := telemetryProviderTestApp(t)
	configuration.Set("telemetry", map[string]interface{}{"enable": false})
	provider := NewProvider()
	if err := provider.Initialize(app); err != nil {
		t.Fatalf("关闭 OTLP 时初始化不应失败: %v", err)
	}
	tracing, err := framework.ResolveServiceAs[*telemetry.Tracing](app, framework.ServiceTelemetry)
	if err != nil || tracing.Enabled() {
		t.Fatalf("关闭 OTLP 时必须保留禁用追踪服务: tracing=%v err=%v", tracing, err)
	}
	if err := provider.Shutdown(app); err != nil {
		t.Fatalf("关闭未启用 OTLP Provider 应幂等成功: %v", err)
	}
}

// TestProviderLifecycleRejectsInvalidArgumentsAndDuplicateInitialization 验证服务提供者的
// 注册、启动和初始化边界，不允许 nil 应用或重复初始化留下半成品 SDK。
func TestProviderLifecycleRejectsInvalidArgumentsAndDuplicateInitialization(t *testing.T) {
	var nilProvider *Provider
	if err := nilProvider.Register(nil); err != nil {
		t.Fatalf("Register 空 Provider 不应产生伪错误: %v", err)
	}
	if err := nilProvider.Boot(nil); err != nil {
		t.Fatalf("Boot 空 Provider 不应产生伪错误: %v", err)
	}
	if err := nilProvider.Shutdown(nil); err != nil {
		t.Fatalf("Shutdown 空 Provider 不应失败: %v", err)
	}
	provider := NewProvider()
	if err := provider.Initialize(nil); !errors.Is(err, framework.ErrNilApplication) {
		t.Fatalf("空应用必须返回稳定错误: %v", err)
	}
	app, configuration := telemetryProviderTestApp(t)
	configuration.Set("telemetry", map[string]interface{}{"enable": false})
	if err := provider.Register(app); err != nil {
		t.Fatalf("Register 不应创建 exporter: %v", err)
	}
	if err := provider.Boot(app); err != nil {
		t.Fatalf("Boot 不应执行网络探测: %v", err)
	}
	if err := provider.Initialize(app); err != nil {
		t.Fatalf("首次初始化失败: %v", err)
	}
	if err := provider.Initialize(app); err == nil {
		t.Fatal("重复初始化必须失败")
	}
}

// TestProviderShutdownBeforeInitializeClosesLifecycle 验证提前关闭不会允许后续创建无法回收的 SDK。
func TestProviderShutdownBeforeInitializeClosesLifecycle(t *testing.T) {
	provider := NewProvider()
	if err := provider.Shutdown(nil); err != nil {
		t.Fatalf("初始化前关闭 Provider 失败: %v", err)
	}
	app, configuration := telemetryProviderTestApp(t)
	configuration.Set("telemetry", map[string]interface{}{"enable": false})
	if err := provider.Initialize(app); !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("关闭后的 Provider 必须拒绝初始化，实际为 %v", err)
	}
	if err := provider.Shutdown(app); err != nil {
		t.Fatalf("重复关闭 Provider 应保持幂等，实际为 %v", err)
	}
}
