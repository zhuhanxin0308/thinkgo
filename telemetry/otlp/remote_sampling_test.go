package otlp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestOTLPRemoteTraceFlagsCannotOverrideSamplingRatio(t *testing.T) {
	const upstreamTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	collector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	for _, test := range []struct {
		name, flags string
		ratio       float64
		wantSample  bool
	}{
		{name: "远程标志不能强制采样", flags: "01"},
		{name: "远程标志不能强制丢样", flags: "00", ratio: 1, wantSample: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, cfg := telemetryProviderTestApp(t)
			if err := cfg.Set("telemetry", map[string]any{
				"enable": true, "endpoint": collector.URL + "/v1/traces", "sample_ratio": test.ratio,
			}); err != nil {
				t.Fatal(err)
			}
			provider := NewProvider()
			if err := provider.Initialize(app); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := provider.Shutdown(app); err != nil {
					t.Error(err)
				}
			})
			tracing, err := framework.ResolveServiceAs[*telemetry.Tracing](app, framework.ServiceTelemetry)
			if err != nil {
				t.Fatal(err)
			}
			header := make(http.Header)
			header.Set("traceparent", "00-"+upstreamTraceID+"-00f067aa0ba902b7-"+test.flags)
			ctx, span := tracing.StartServer(t.Context(), header, http.MethodGet, "/orders", "example.test")
			telemetry.EndHTTPServer(ctx, http.StatusOK, nil)
			if actual := span.SpanContext().IsSampled(); actual != test.wantSample {
				t.Fatalf("采样率 %.1f 与 traceparent 标志 %s 的实际决策为 %t", test.ratio, test.flags, actual)
			}
			if actual := span.SpanContext().TraceID().String(); actual != upstreamTraceID {
				t.Fatalf("远程父链路关联丢失: %s", actual)
			}
		})
	}
}

func TestRemoteSamplingDrawDoesNotDependOnTraceIDOrFlag(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("00000000000000010000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex("0000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	state, err := trace.ParseTraceState("vendor=example")
	if err != nil {
		t.Fatal(err)
	}
	remoteDraw := 0.1
	draws := 0
	policy := newSamplingPolicy(0.25, func() float64 {
		draws++
		return remoteDraw
	})
	for _, test := range []struct {
		flags    trace.TraceFlags
		remote   bool
		draw     float64
		decision sdktrace.SamplingDecision
	}{
		{remote: true, draw: 0.1, decision: sdktrace.RecordAndSample},
		{remote: true, flags: trace.FlagsSampled, draw: 0.9, decision: sdktrace.Drop},
		{remote: true, flags: trace.FlagsSampled, draw: 0.1, decision: sdktrace.RecordAndSample},
		{remote: true, draw: 0.9, decision: sdktrace.Drop},
		{flags: trace.FlagsSampled, draw: 0.9, decision: sdktrace.RecordAndSample},
		{draw: 0.1, decision: sdktrace.Drop},
	} {
		remoteDraw = test.draw
		parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: test.flags, TraceState: state, Remote: test.remote})
		ctx := trace.ContextWithSpanContext(context.Background(), parent)
		result := policy.ShouldSample(sdktrace.SamplingParameters{ParentContext: ctx, TraceID: traceID})
		if result.Decision != test.decision || result.Tracestate.String() != state.String() {
			t.Fatalf("同一 TraceID 的远程决策或本地继承错误: %#v", result)
		}
	}
	if draws != 4 || policy.Description() == "" {
		t.Fatalf("只有远程父链路应独立取样，实际取样 %d 次", draws)
	}
}
