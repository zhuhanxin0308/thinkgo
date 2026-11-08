package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
	frameworkLog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

type recoveryCountingError struct {
	calls atomic.Int32
}

func (err *recoveryCountingError) Error() string {
	err.calls.Add(1)
	return "render failed password=abcdefgh"
}

type recoveryCountingRenderer struct {
	failure *recoveryCountingError
	reports int
	renders int
}

func (renderer *recoveryCountingRenderer) Report(any) {
	renderer.reports++
}

func (renderer *recoveryCountingRenderer) Render(writer http.ResponseWriter, _ *http.Request, _ any) error {
	renderer.renders++
	writer.Header().Set("X-Partial", "private")
	_, _ = writer.Write([]byte("private partial response"))
	return renderer.failure
}

type recoveryLevelLogger struct {
	recoveryTestLogger
	enabled bool
}

func (logger *recoveryLevelLogger) IsLevelEnabled(level string) bool {
	return logger.enabled && level == frameworkLog.LevelError
}

// TestRecoveryRenderFailureHonorsOptionalLogLevels 验证关闭日志不求值渲染错误，同时自定义报告与渲染仍各执行一次。
func TestRecoveryRenderFailureHonorsOptionalLogLevels(t *testing.T) {
	filtered := frameworkLog.NewLog()
	filtered.SetLevels([]string{frameworkLog.LevelInfo})
	t.Cleanup(func() { _ = filtered.Close() })
	disabled := &recoveryLevelLogger{}
	enabled := &recoveryLevelLogger{enabled: true}
	legacy := &recoveryTestLogger{}
	for _, testCase := range []struct {
		name    string
		logger  exception.Logger
		capture *recoveryTestLogger
		calls   int32
	}{
		{name: "未配置日志"},
		{name: "真实日志级别过滤", logger: filtered},
		{name: "可选能力禁用", logger: disabled, capture: &disabled.recoveryTestLogger},
		{name: "可选能力开启", logger: enabled, capture: &enabled.recoveryTestLogger, calls: 1},
		{name: "旧日志接口兼容", logger: legacy, capture: legacy, calls: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			failure := &recoveryCountingError{}
			renderer := &recoveryCountingRenderer{failure: failure}
			request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/orders", nil))
			t.Cleanup(func() { _ = request.Cleanup() })
			response := (&Recovery{Log: testCase.logger, Handler: renderer}).Handle(request, func(*fwcontext.Request) *fwcontext.Response {
				panic("original business failure")
			})
			if renderer.reports != 1 || renderer.renders != 1 {
				t.Errorf("日志过滤不得跳过或重复自定义异常处理器: reports=%d renders=%d", renderer.reports, renderer.renders)
			}
			if response == nil || response.GetStatus() != http.StatusInternalServerError || string(response.GetBody()) != http.StatusText(http.StatusInternalServerError) || response.GetHeader("X-Partial") != "" {
				t.Fatalf("日志过滤改变了失败渲染的安全响应: %#v", response)
			}
			if calls := failure.calls.Load(); calls != testCase.calls {
				t.Errorf("渲染错误求值次数错误: got=%d want=%d", calls, testCase.calls)
			}
			if testCase.capture == nil {
				return
			}
			if calls := len(testCase.capture.errorCalls); calls != int(testCase.calls) {
				t.Fatalf("渲染错误日志数量错误: got=%d want=%d", calls, testCase.calls)
			}
			if testCase.calls != 0 && testCase.capture.errorCalls[0].ctx["error"] != "render failed password=abcd[REDACTED]" {
				t.Errorf("开启日志后未保留原有凭据脱敏: %#v", testCase.capture.errorCalls[0])
			}
		})
	}
}
