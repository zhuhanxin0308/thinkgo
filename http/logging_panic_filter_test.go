package http

import (
	stdcontext "context"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
	frameworklog "github.com/zhuhanxin0308/thinkgo/v3/log"
	fwtelemetry "github.com/zhuhanxin0308/thinkgo/v3/telemetry"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestCommittedPanicFiltersOnlyLogFormatting 验证关闭错误日志只跳过日志专用求值，已提交响应与异常 Span 仍完整收尾。
func TestCommittedPanicFiltersOnlyLogFormatting(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		disabled  bool
		level     string
		wantCalls int32
		wantLogs  int
	}{
		{name: "关闭日志", disabled: true, wantCalls: 1},
		{name: "过滤ERROR", level: frameworklog.LevelWarning, wantCalls: 1},
		{name: "开启ERROR", level: frameworklog.LevelError, wantCalls: 2, wantLogs: 1},
	} {
		for _, bounded := range []bool{false, true} {
			mode := "非阻塞收尾"
			if bounded {
				mode = "监督收尾"
			}
			t.Run(testCase.name+"/"+mode, func(t *testing.T) {
				driver := &accessLogDriver{}
				logger := frameworklog.NewLog()
				if testCase.disabled {
					logger = frameworklog.NewDisabledLog()
				} else {
					logger.SetLevels([]string{testCase.level})
				}
				if err := logger.AddDriver(driver); err != nil {
					t.Fatalf("添加 panic 日志观测驱动失败: %v", err)
				}
				t.Cleanup(func() { _ = logger.Close() })

				spanRecorder := tracetest.NewSpanRecorder()
				provider := sdktrace.NewTracerProvider(
					sdktrace.WithSpanProcessor(spanRecorder),
					sdktrace.WithSampler(sdktrace.AlwaysSample()),
				)
				t.Cleanup(func() { _ = provider.Shutdown(stdcontext.Background()) })
				tracing, err := fwtelemetry.New(fwtelemetry.Config{
					Enabled: true, Provider: provider, Propagator: propagation.TraceContext{},
					InstrumentationName: "thinkgo/committed-panic-log-test",
				})
				if err != nil {
					t.Fatalf("创建 panic 收尾追踪器失败: %v", err)
				}
				raw := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/stream", nil)
				traceContext, _ := tracing.StartServer(raw.Context(), raw.Header, raw.Method, raw.URL.Path, raw.Host)
				raw = raw.WithContext(traceContext)
				recorder := httptest.NewRecorder()
				writer := newStatusTrackingResponseWriter(recorder)
				writer.WriteHeader(stdhttp.StatusAccepted)
				const committedBody = "already sent"
				if _, err := writer.Write([]byte(committedBody)); err != nil {
					t.Fatalf("提交测试响应失败: %v", err)
				}
				handler := &Http{log: logger, telemetry: tracing}
				endCalls := 0
				if bounded {
					handler.event = event.NewDispatcher()
					if err := handler.event.Listen(event.EventHttpEnd, &event.SimpleListener{Handler: func(event.Event) error {
						endCalls++
						return nil
					}}); err != nil {
						t.Fatalf("注册收尾监督监听器失败: %v", err)
					}
				}
				state := &requestServeState{
					raw: raw, response: fwcontext.NewCommittedResponse(stdhttp.StatusAccepted),
					statusWriter: writer, writer: writer, startedAt: time.Now(),
				}
				failure := &disabledLogCountingError{}
				handler.finishRequest(state, failure)
				if calls := failure.calls.Load(); calls != testCase.wantCalls {
					t.Errorf("生命周期必须保留一次求值，日志仅在开启时额外求值: got=%d want=%d", calls, testCase.wantCalls)
				}
				if !state.abortResponse || recorder.Code != stdhttp.StatusAccepted || recorder.Body.String() != committedBody {
					t.Fatalf("日志过滤改变了已提交响应或中止语义: abort=%t status=%d body=%q", state.abortResponse, recorder.Code, recorder.Body.String())
				}
				if bounded && endCalls != 1 {
					t.Fatalf("日志过滤跳过了监督生命周期，实际执行 %d 次", endCalls)
				}
				spans := spanRecorder.Ended()
				if len(spans) != 1 || spans[0].Status().Code != codes.Error || len(spans[0].Events()) != 1 {
					t.Fatalf("日志过滤导致 panic Span 未正确结束: %#v", spans)
				}
				if payload := fmt.Sprint(spans[0].Events()); strings.Contains(payload, "abcdefgh") {
					t.Fatalf("panic Span 泄露原始凭据: %s", payload)
				}
				if err := logger.Flush(stdcontext.Background()); err != nil {
					t.Fatalf("刷新 panic 日志失败: %v", err)
				}
				entries := driver.allEntries()
				if len(entries) != testCase.wantLogs {
					t.Fatalf("panic 日志数量错误: got=%d want=%d", len(entries), testCase.wantLogs)
				}
				if len(entries) > 0 && (entries[0].Message != "响应写出后发生异常" || entries[0].Context["error"] != "request failed password=abcd[REDACTED]") {
					t.Fatalf("开启 panic 日志后未保留原有消息与脱敏: %#v", entries[0])
				}
			})
		}
	}
}
