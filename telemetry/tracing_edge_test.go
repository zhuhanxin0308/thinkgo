package telemetry

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestTracingRejectsIncompleteEnabledConfiguration 验证启用追踪时三个依赖必须同时存在，
// 关闭追踪则保持零开销 no-op 语义。
func TestTracingRejectsIncompleteEnabledConfiguration(t *testing.T) {
	cases := []Config{
		{Enabled: true, Propagator: propagation.TraceContext{}, InstrumentationName: "test"},
		{Enabled: true, Provider: sdktrace.NewTracerProvider(), InstrumentationName: "test"},
		{Enabled: true, Provider: sdktrace.NewTracerProvider(), Propagator: propagation.TraceContext{}},
	}
	for index, config := range cases {
		if _, err := New(config); !errors.Is(err, ErrInvalidTracingConfig) {
			t.Fatalf("不完整追踪配置 %d 必须失败: %v", index, err)
		}
	}
	tracing, err := New(Config{Enabled: false, InstrumentationName: ""})
	if err != nil || tracing == nil || tracing.Enabled() {
		t.Fatalf("关闭追踪必须返回稳定 no-op: tracing=%v err=%v", tracing, err)
	}
}

// TestTracingNormalizesEmptyRequestAndClosesSpanOnce 验证空请求字段、非法状态码、
// 空路由和重复结束都不会产生不确定 Span 状态。
func TestTracingNormalizesEmptyRequestAndClosesSpanOnce(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracing, err := New(Config{
		Enabled:             true,
		Provider:            provider,
		Propagator:          propagation.TraceContext{},
		InstrumentationName: "thinkgo-test",
	})
	if err != nil {
		t.Fatalf("创建边界追踪器失败: %v", err)
	}
	var nilContext context.Context
	ctx, span := tracing.StartServer(nilContext, nil, " ", " ", " ")
	if span == nil || SpanFromContext(ctx) != span {
		t.Fatalf("空请求字段仍必须创建可关联 Span: span=%v", span)
	}
	SetHTTPRoute(ctx, " ")
	SetHTTPRoute(ctx, "/health")
	EndHTTPServer(ctx, 700, nil)
	EndHTTPServer(ctx, http.StatusOK, errors.New("late error must be ignored"))

	ended := recorder.Ended()
	if len(ended) != 1 || ended[0].Name() != "HTTP /health" || ended[0].Status().Code.String() != "Error" {
		t.Fatalf("边界 Span 状态错误: %#v", ended)
	}
	var nilSpan *HTTPSpan
	if nilSpan.SpanContext().IsValid() || SpanFromContext(nilContext) != nil {
		t.Fatal("空 Span 访问必须安全返回零值")
	}
	SetHTTPRoute(nilContext, "/ignored")
	EndHTTPServer(nilContext, http.StatusOK, nil)
}
