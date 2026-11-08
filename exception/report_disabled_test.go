package exception

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	frameworkLog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

const disabledReportAllocationSamples = 100

type reportCountingError struct {
	calls atomic.Int32
}

func (err *reportCountingError) Error() string {
	err.calls.Add(1)
	return "internal failure password=abcdefgh"
}

// TestReportDisabledSkipsFieldsAndAllocations 验证报告级别被过滤后不求值错误、不复制日志数据，也不创建堆栈。
func TestReportDisabledSkipsFieldsAndAllocations(t *testing.T) {
	logger := frameworkLog.NewLog()
	logger.SetLevels([]string{frameworkLog.LevelInfo})
	t.Cleanup(func() { _ = logger.Close() })
	handler := &Handle{Log: logger}
	failure := &reportCountingError{}
	for _, testCase := range []struct {
		name  string
		value any
	}{
		{name: "普通错误", value: failure},
		{name: "HTTP客户端错误", value: NewHttpException(http.StatusForbidden, "password=abcdefgh")},
		{name: "HTTP服务端错误", value: NewHttpException(http.StatusServiceUnavailable, "password=abcdefgh")},
		{name: "验证错误", value: NewValidateException("password=abcdefgh", "password")},
		{name: "业务客户端错误", value: NewBusinessException(http.StatusConflict, "password=abcdefgh").WithStatus(http.StatusConflict).WithCause(failure).WithData(map[string]any{"order_id": "order-1"})},
		{name: "业务服务端错误", value: NewBusinessException(http.StatusServiceUnavailable, "password=abcdefgh").WithStatus(http.StatusServiceUnavailable).WithCause(failure).WithData(map[string]any{"order_id": "order-1"})},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			failure.calls.Store(0)
			allocations := testing.AllocsPerRun(disabledReportAllocationSamples, func() { handler.Report(testCase.value) })
			if calls := failure.calls.Load(); calls != 0 {
				t.Errorf("禁用报告不应求值错误或业务 cause，实际调用 %d 次", calls)
			}
			if allocations != 0 {
				t.Errorf("禁用报告不应创建日志字段、数据副本或堆栈，实际每次 %.2f 次分配", allocations)
			}
		})
	}
}

type reportLevelLogger struct {
	mockExceptionLogger
	enabled string
}

func (logger *reportLevelLogger) IsLevelEnabled(level string) bool {
	return level == logger.enabled
}

// TestReportFiltersActualExceptionLevel 验证可选级别能力按实际状态过滤，保留其他级别和原有堆栈策略。
func TestReportFiltersActualExceptionLevel(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		level string
		value any
	}{
		{name: "普通错误", level: frameworkLog.LevelError, value: fmt.Errorf("internal failure")},
		{name: "HTTP客户端错误", level: frameworkLog.LevelWarning, value: NewHttpException(http.StatusForbidden, "forbidden")},
		{name: "HTTP服务端错误", level: frameworkLog.LevelError, value: NewHttpException(http.StatusServiceUnavailable, "unavailable")},
		{name: "验证错误", level: frameworkLog.LevelWarning, value: NewValidateException("invalid", "name")},
		{name: "业务客户端错误", level: frameworkLog.LevelWarning, value: NewBusinessException(http.StatusConflict, "conflict").WithStatus(http.StatusConflict)},
		{name: "业务服务端错误", level: frameworkLog.LevelError, value: NewBusinessException(http.StatusServiceUnavailable, "unavailable").WithStatus(http.StatusServiceUnavailable)},
		{name: "无效业务状态降级", level: frameworkLog.LevelError, value: NewBusinessException(http.StatusOK, "invalid status").WithStatus(http.StatusOK)},
		{name: "包装验证错误", level: frameworkLog.LevelWarning, value: fmt.Errorf("wrapped: %w", NewValidateException("invalid", "name"))},
	} {
		for _, enabled := range []string{frameworkLog.LevelError, frameworkLog.LevelWarning} {
			t.Run(testCase.name+"/"+enabled, func(t *testing.T) {
				logger := &reportLevelLogger{enabled: enabled}
				(&Handle{Log: logger}).Report(testCase.value)
				calls := append(logger.errorCalls, logger.warningCalls...)
				if testCase.level != enabled {
					if len(calls) != 0 {
						t.Errorf("级别 %s 已过滤，仍记录 %d 条日志", testCase.level, len(calls))
					}
					return
				}
				if len(calls) != 1 {
					t.Fatalf("开启级别应记录一次，实际为 %d", len(calls))
				}
				if enabled == frameworkLog.LevelError && (len(logger.errorCalls) != 1 || calls[0].ctx["stack"] == nil) {
					t.Error("服务端错误必须保留 error 级别和堆栈")
				}
				if enabled == frameworkLog.LevelWarning && (len(logger.warningCalls) != 1 || calls[0].ctx["stack"] != nil) {
					t.Error("客户端错误必须保留 warning 级别且不创建堆栈")
				}
			})
		}
	}
}

// TestDisabledReportKeepsRenderingIndependent 验证关闭日志不会吞掉业务响应，也不跳过响应所需的敏感数据处理。
func TestDisabledReportKeepsRenderingIndependent(t *testing.T) {
	logger := frameworkLog.NewLog()
	logger.SetLevels([]string{frameworkLog.LevelInfo})
	t.Cleanup(func() { _ = logger.Close() })
	failure := &reportCountingError{}
	reported := NewBusinessException(http.StatusConflict, "库存不足").WithStatus(http.StatusConflict).
		WithCause(failure).WithData(map[string]any{"order_id": "order-1"})
	handler := &Handle{Log: logger}
	request := httptest.NewRequest(http.MethodGet, "http://example.com/orders", nil)
	request.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()
	if err := ReportAndRender(handler, recorder, request, reported); err != nil {
		t.Fatalf("禁用日志后的业务异常渲染失败: %v", err)
	}
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"order_id":"order-1"`) || !strings.Contains(recorder.Body.String(), "库存不足") {
		t.Fatalf("禁用日志改变了业务响应: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "abcdefgh") || strings.Contains(recorder.Body.String(), "internal failure") {
		t.Fatalf("关闭报告不得改变响应的敏感信息边界: %s", recorder.Body.String())
	}
	if calls := failure.calls.Load(); calls != 0 {
		t.Errorf("禁用日志不应为了报告或渲染读取内部 cause，实际调用 %d 次", calls)
	}
	serverFailure := NewBusinessException(http.StatusServiceUnavailable, "password=abcdefgh").
		WithStatus(http.StatusServiceUnavailable).WithData(map[string]any{"password": "abcdefgh"})
	serverRecorder := httptest.NewRecorder()
	if err := ReportAndRender(handler, serverRecorder, request, serverFailure); err != nil {
		t.Fatalf("禁用日志后的服务端异常渲染失败: %v", err)
	}
	if serverRecorder.Code != http.StatusServiceUnavailable || strings.Contains(serverRecorder.Body.String(), "abcdefgh") || !strings.Contains(serverRecorder.Body.String(), http.StatusText(http.StatusInternalServerError)) {
		t.Fatalf("关闭报告改变了服务端异常的隐藏策略: status=%d body=%s", serverRecorder.Code, serverRecorder.Body.String())
	}
}
