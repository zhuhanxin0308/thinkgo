package telemetry

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var (
	// ErrInvalidTracingConfig 表示启用追踪时缺少 Provider、传播器或仪表名称。
	ErrInvalidTracingConfig = errors.New("invalid tracing config")
	errRequestFailure       = errors.New("request processing failed")
)

// Config 描述一个不拥有 SDK 生命周期的框架追踪入口。
type Config struct {
	Enabled                bool
	Provider               trace.TracerProvider
	Propagator             propagation.TextMapPropagator
	InstrumentationName    string
	InstrumentationVersion string
}

// Tracing 封装框架使用的 Tracer 和 W3C 上下文传播器。
type Tracing struct {
	enabled    bool
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
}

type httpSpanContextKey struct{}

// HTTPSpan 保存一次服务端请求 Span 及其稳定命名信息。
type HTTPSpan struct {
	span   trace.Span
	method string
	ended  atomic.Bool
}

// Disabled 返回不会创建 Span 或解析传播头的默认追踪入口。
func Disabled() *Tracing {
	return &Tracing{}
}

// New 创建追踪入口；SDK Provider 生命周期仍由注册它的应用 Provider 管理。
func New(config Config) (*Tracing, error) {
	if !config.Enabled {
		return Disabled(), nil
	}
	if config.Provider == nil || config.Propagator == nil || strings.TrimSpace(config.InstrumentationName) == "" {
		return nil, ErrInvalidTracingConfig
	}
	options := make([]trace.TracerOption, 0, 1)
	if version := strings.TrimSpace(config.InstrumentationVersion); version != "" {
		options = append(options, trace.WithInstrumentationVersion(version))
	}
	return &Tracing{
		enabled:    true,
		tracer:     config.Provider.Tracer(strings.TrimSpace(config.InstrumentationName), options...),
		propagator: config.Propagator,
	}, nil
}

// Enabled 判断当前应用是否显式启用了分布式追踪。
func (tracing *Tracing) Enabled() bool {
	return tracing != nil && tracing.enabled && tracing.tracer != nil && tracing.propagator != nil
}

// StartServer 提取远程父上下文并创建 HTTP 服务端 Span。
func (tracing *Tracing) StartServer(
	ctx context.Context,
	header http.Header,
	method string,
	path string,
	host string,
) (context.Context, *HTTPSpan) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !tracing.Enabled() {
		return ctx, nil
	}
	ctx = tracing.propagator.Extract(ctx, propagation.HeaderCarrier(header))
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = "HTTP"
	}
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/"
	}
	ctx, span := tracing.tracer.Start(
		ctx,
		method,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.request.method", method),
			attribute.String("url.path", path),
			attribute.String("server.address", strings.TrimSpace(host)),
		),
	)
	httpSpan := &HTTPSpan{span: span, method: method}
	ctx = context.WithValue(ctx, httpSpanContextKey{}, httpSpan)
	return ctx, httpSpan
}

// SpanContext 返回底层 OpenTelemetry SpanContext，供日志关联和测试使用。
func (span *HTTPSpan) SpanContext() trace.SpanContext {
	if span == nil || span.span == nil {
		return trace.SpanContext{}
	}
	return span.span.SpanContext()
}

// SpanFromContext 返回 ThinkGo 创建的 HTTP 服务端 Span；未启用时返回 nil。
func SpanFromContext(ctx context.Context) *HTTPSpan {
	if ctx == nil {
		return nil
	}
	span, _ := ctx.Value(httpSpanContextKey{}).(*HTTPSpan)
	return span
}

// SetHTTPRoute 在路由匹配后把低基数模板写入 Span 名称和属性。
func SetHTTPRoute(ctx context.Context, route string) {
	span := SpanFromContext(ctx)
	if span == nil || span.span == nil || span.ended.Load() {
		return
	}
	route = strings.TrimSpace(route)
	if route == "" {
		return
	}
	span.span.SetName(span.method + " " + route)
	span.span.SetAttributes(attribute.String("http.route", route))
}

// EndHTTPServer 幂等记录响应状态、异常并结束服务端 Span。
func EndHTTPServer(ctx context.Context, status int, requestErr error) {
	span := SpanFromContext(ctx)
	if span == nil || span.span == nil || !span.ended.CompareAndSwap(false, true) {
		return
	}
	if status < http.StatusContinue || status > 599 {
		status = http.StatusInternalServerError
	}
	span.span.SetAttributes(attribute.Int("http.response.status_code", status))
	if requestErr != nil {
		// 请求错误可能携带凭据、SQL、路径或个人数据，遥测边界只导出稳定分类。
		span.span.RecordError(errRequestFailure)
		span.span.SetStatus(codes.Error, errRequestFailure.Error())
	} else if status >= http.StatusInternalServerError {
		span.span.SetStatus(codes.Error, http.StatusText(status))
	}
	span.span.End()
}
