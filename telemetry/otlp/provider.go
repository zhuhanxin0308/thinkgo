package otlp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/config"
	"github.com/zhuhanxin0308/thinkgo/framework/telemetry"
	"github.com/zhuhanxin0308/thinkgo/framework/version"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const providerShutdownTimeout = 10 * time.Second

// Provider 根据最终应用配置创建并拥有 OTLP/HTTP Trace SDK 生命周期。
type Provider struct {
	mu                 sync.Mutex
	state              providerState
	initializationDone chan struct{}
	shutdownDone       chan struct{}
	sdk                *sdktrace.TracerProvider
	shutdownErr        error
}

type providerState uint8

const (
	providerNew providerState = iota
	providerInitializing
	providerReady
	providerFailed
	providerClosing
	providerClosed
)

// ErrProviderClosed 表示 Provider 已进入不可重新初始化的关闭终态。
var ErrProviderClosed = errors.New("OTLP Provider 已关闭")

var (
	_ framework.ServiceProvider            = (*Provider)(nil)
	_ framework.ServiceProviderInitializer = (*Provider)(nil)
	_ framework.ServiceProviderShutdown    = (*Provider)(nil)
)

// NewProvider 创建尚未读取应用配置的 OTLP Provider。
func NewProvider() *Provider {
	return &Provider{}
}

// Register 不在配置合并前创建 exporter。
func (provider *Provider) Register(*framework.App) error { return nil }

// Initialize 读取 telemetry 配置，关闭时保留无分配的 No-op 追踪服务。
func (provider *Provider) Initialize(app *framework.App) (initializeErr error) {
	if provider == nil || app == nil {
		return framework.ErrNilApplication
	}
	if err := provider.beginInitialization(); err != nil {
		return err
	}
	defer func() {
		provider.finishInitialization(initializeErr)
	}()
	configuration, err := framework.ResolveServiceAs[*config.Config](app, framework.ServiceConfig)
	if err != nil {
		return fmt.Errorf("遥测配置服务不可用: %w", err)
	}
	values := configuration.GetMap("telemetry")
	parsed, err := parseProviderConfig(
		values,
		app.AppName,
		version.Number,
		configuration.GetString("app.app_env", "development"),
	)
	if err != nil {
		return err
	}
	if !parsed.Enabled {
		if err := app.Instance(string(framework.ServiceTelemetry), telemetry.Disabled()); err != nil {
			return fmt.Errorf("绑定禁用遥测服务失败: %w", err)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), parsed.Timeout)
	defer cancel()
	exporterOptions := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(parsed.Endpoint),
		otlptracehttp.WithTimeout(parsed.Timeout),
	}
	if len(parsed.Headers) > 0 {
		exporterOptions = append(exporterOptions, otlptracehttp.WithHeaders(parsed.Headers))
	}
	exporter, err := otlptracehttp.New(ctx, exporterOptions...)
	if err != nil {
		return fmt.Errorf("创建 OTLP HTTP exporter 失败: %w", err)
	}
	resources, err := resource.Merge(
		resource.Environment(),
		resource.NewSchemaless(
			attribute.String("service.name", parsed.ServiceName),
			attribute.String("service.version", parsed.ServiceVersion),
			attribute.String("deployment.environment.name", parsed.Environment),
		),
	)
	if err != nil {
		_ = exporter.Shutdown(ctx)
		return fmt.Errorf("创建 OpenTelemetry Resource 失败: %w", err)
	}
	sdk := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resources),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(parsed.SampleRatio))),
		sdktrace.WithBatcher(
			exporter,
			sdktrace.WithBatchTimeout(parsed.BatchTimeout),
			sdktrace.WithMaxExportBatchSize(parsed.MaxExportBatchSize),
			sdktrace.WithMaxQueueSize(parsed.MaxQueueSize),
		),
	)
	provider.mu.Lock()
	provider.sdk = sdk
	provider.mu.Unlock()
	tracing, err := telemetry.New(telemetry.Config{
		Enabled:                true,
		Provider:               sdk,
		Propagator:             propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
		InstrumentationName:    "github.com/zhuhanxin0308/thinkgo/framework/http",
		InstrumentationVersion: version.Number,
	})
	if err != nil {
		shutdownErr := sdk.Shutdown(ctx)
		provider.clearSDK(sdk)
		return errors.Join(err, shutdownErr)
	}
	if err := app.Instance(string(framework.ServiceTelemetry), tracing); err != nil {
		shutdownErr := sdk.Shutdown(ctx)
		provider.clearSDK(sdk)
		return errors.Join(fmt.Errorf("绑定遥测服务失败: %w", err), shutdownErr)
	}
	return nil
}

func (provider *Provider) beginInitialization() error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	switch provider.state {
	case providerNew:
		provider.state = providerInitializing
		provider.initializationDone = make(chan struct{})
		return nil
	case providerClosing, providerClosed:
		return ErrProviderClosed
	default:
		return errors.New("OTLP Provider 已初始化")
	}
}

func (provider *Provider) finishInitialization(initializeErr error) {
	provider.mu.Lock()
	if provider.state == providerInitializing {
		if initializeErr != nil {
			provider.state = providerFailed
		} else {
			provider.state = providerReady
		}
	}
	done := provider.initializationDone
	provider.initializationDone = nil
	if done != nil {
		close(done)
	}
	provider.mu.Unlock()
}

func (provider *Provider) clearSDK(sdk *sdktrace.TracerProvider) {
	provider.mu.Lock()
	if provider.sdk == sdk {
		provider.sdk = nil
	}
	provider.mu.Unlock()
}

// Boot 不执行网络探测；Exporter 在批次导出时按 SDK 重试策略连接 Collector。
func (*Provider) Boot(*framework.App) error { return nil }

// Shutdown 在有界上下文中刷新并关闭 Provider 拥有的 Trace SDK。
func (provider *Provider) Shutdown(*framework.App) error {
	if provider == nil {
		return nil
	}
	for {
		provider.mu.Lock()
		switch provider.state {
		case providerNew:
			provider.state = providerClosed
			provider.mu.Unlock()
			return nil
		case providerInitializing:
			done := provider.initializationDone
			provider.mu.Unlock()
			if done != nil {
				<-done
			}
			continue
		case providerClosing:
			done := provider.shutdownDone
			provider.mu.Unlock()
			if done != nil {
				<-done
			}
			provider.mu.Lock()
			err := provider.shutdownErr
			provider.mu.Unlock()
			return err
		case providerClosed:
			err := provider.shutdownErr
			provider.mu.Unlock()
			return err
		case providerReady, providerFailed:
			provider.state = providerClosing
			provider.shutdownDone = make(chan struct{})
			sdk := provider.sdk
			provider.sdk = nil
			provider.mu.Unlock()

			var shutdownErr error
			if sdk != nil {
				ctx, cancel := context.WithTimeout(context.Background(), providerShutdownTimeout)
				shutdownErr = sdk.Shutdown(ctx)
				cancel()
			}
			provider.mu.Lock()
			provider.shutdownErr = shutdownErr
			provider.state = providerClosed
			done := provider.shutdownDone
			provider.shutdownDone = nil
			if done != nil {
				close(done)
			}
			provider.mu.Unlock()
			return shutdownErr
		}
	}
}
