package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestTracingDoesNotExportRawRequestErrors 验证凭据、路径和数据库错误不会进入遥测载荷。
func TestTracingDoesNotExportRawRequestErrors(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracing, err := New(Config{
		Enabled:             true,
		Provider:            provider,
		Propagator:          propagation.TraceContext{},
		InstrumentationName: "example.com/thinkgo-test",
	})
	if err != nil {
		t.Fatalf("创建追踪器失败: %v", err)
	}
	ctx, _ := tracing.StartServer(context.Background(), http.Header{}, http.MethodGet, "/private", "api.example.com")
	secret := "database password=top-secret path=C:/private/users.db"
	EndHTTPServer(ctx, http.StatusInternalServerError, errors.New(secret))
	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("服务端 Span 数量错误: %d", len(ended))
	}
	current := ended[0]
	payload := current.Status().Description
	for _, event := range current.Events() {
		payload += fmt.Sprint(event.Attributes)
	}
	if strings.Contains(payload, "top-secret") || strings.Contains(payload, "users.db") || strings.Contains(payload, "database password") {
		t.Fatalf("遥测载荷不得包含原始请求错误: %q", payload)
	}
}

// TestTracingCreatesServerSpanAndExtractsRemoteParent 验证 W3C 父上下文、路由命名、状态和错误记录进入服务端 Span。
func TestTracingCreatesServerSpanAndExtractsRemoteParent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracing, err := New(Config{
		Enabled:                true,
		Provider:               provider,
		Propagator:             propagation.TraceContext{},
		InstrumentationName:    "example.com/thinkgo-test",
		InstrumentationVersion: "2.0.0",
	})
	if err != nil {
		t.Fatalf("创建追踪器失败: %v", err)
	}
	header := http.Header{}
	header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ctx, span := tracing.StartServer(context.Background(), header, "GET", "/users/42", "api.example.com")
	if span == nil || !span.SpanContext().IsValid() {
		t.Fatal("启用追踪时必须创建有效 Span")
	}
	SetHTTPRoute(ctx, "/users/:id")
	EndHTTPServer(ctx, http.StatusInternalServerError, errors.New("database unavailable"))

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("服务端 Span 数量错误: %d", len(ended))
	}
	current := ended[0]
	if current.Name() != "GET /users/:id" || !current.Parent().IsRemote() {
		t.Fatalf("Span 路由名或远程父上下文错误: name=%q parent=%v", current.Name(), current.Parent())
	}
	if current.Parent().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("远程 TraceID 未传播: %s", current.Parent().TraceID())
	}
	if current.Status().Code.String() != "Error" || len(current.Events()) == 0 {
		t.Fatalf("服务端错误未记录到 Span: status=%v events=%v", current.Status(), current.Events())
	}
}

// TestDisabledTracingDoesNotCreateSpan 验证默认关闭路径不创建遥测数据。
func TestDisabledTracingDoesNotCreateSpan(t *testing.T) {
	tracing := Disabled()
	ctx, span := tracing.StartServer(context.Background(), http.Header{}, "GET", "/", "example.com")
	if span != nil || SpanFromContext(ctx) != nil {
		t.Fatalf("关闭追踪时不应创建 Span: span=%v", span)
	}
}
