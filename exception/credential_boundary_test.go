package exception

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	frameworkLog "github.com/zhuhanxin0308/thinkgo/framework/log"
	logDriver "github.com/zhuhanxin0308/thinkgo/framework/log/driver"
)

// TestExceptionCredentialBoundaries 覆盖独立异常日志路径，避免只测试 log 上下文字段而漏掉主消息。
func TestExceptionCredentialBoundaries(t *testing.T) {
	const message = "Authorization: Bearer tokn-hidden-value\nCookie: sid=sess-hidden-value; other=cook-hidden-value"
	logger := &mockExceptionLogger{}
	handler := &Handle{Log: logger}
	handler.Report(errors.New(message))
	if len(logger.errorCalls) != 1 {
		t.Fatal("未记录异常")
	}
	logged := logger.errorCalls[0].msg
	for _, suffix := range []string{"tokn-hidden-value", "sess-hidden-value", "cook-hidden-value", "hidden-value"} {
		if strings.Contains(logged, suffix) {
			t.Fatalf("异常日志保留凭据后缀: %q", logged)
		}
	}
	for _, prefix := range []string{"tokn[REDACTED]", "sess[REDACTED]", "cook[REDACTED]"} {
		if !strings.Contains(logged, prefix) {
			t.Fatalf("日志未遵守只保留前四字符: %q", logged)
		}
	}
	public := safeExceptionText(errors.New(message))
	for _, fragment := range []string{"tokn", "sess-hidden", "cook-hidden", "hidden-value"} {
		if strings.Contains(public, fragment) {
			t.Fatalf("异常响应必须完整遮蔽凭据: %q", public)
		}
	}
}

// TestExceptionCredentialOutputPipeline 验证真实日志驱动和调试 JSON 响应采用各自的凭据可见性策略。
func TestExceptionCredentialOutputPipeline(t *testing.T) {
	const message = "Authorization: Bearer tokn-private-suffix\nCookie: sid=sess-private-suffix; other=cook-private-suffix"
	var output bytes.Buffer
	driver, err := logDriver.NewConsoleWithWriter(&output)
	if err != nil {
		t.Fatal(err)
	}
	logger := frameworkLog.NewLog(driver)
	handler := &Handle{App: &mockExceptionApp{debug: true}, Log: logger}
	handler.Report(errors.New(message))
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "private-suffix") {
		t.Fatal("异常经过实际日志驱动后仍泄露凭据后缀")
	}
	for _, prefix := range []string{"tokn[REDACTED]", "sess[REDACTED]", "cook[REDACTED]"} {
		if !strings.Contains(output.String(), prefix) {
			t.Fatal("实际日志未保留指定前缀")
		}
	}
	request := httptest.NewRequest(http.MethodGet, "https://example.com", nil)
	request.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()
	if err := handler.Render(recorder, request, errors.New(message)); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Message string `json:"msg"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil || recorder.Code != http.StatusInternalServerError || result.Message == "" {
		t.Fatalf("没有收到有效调试异常响应: status=%d err=%v", recorder.Code, err)
	}
	for _, fragment := range []string{"tokn", "sess-private", "cook-private", "private-suffix"} {
		if strings.Contains(result.Message, fragment) {
			t.Fatal("调试响应错误地沿用了日志前缀策略")
		}
	}
}
