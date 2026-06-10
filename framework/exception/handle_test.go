package exception

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockExceptionApp struct {
	debug bool
}

func (a *mockExceptionApp) IsDebug() bool {
	return a.debug
}

type exceptionLogCall struct {
	msg string
	ctx map[string]interface{}
}

type mockExceptionLogger struct {
	errorCalls   []exceptionLogCall
	warningCalls []exceptionLogCall
}

func (l *mockExceptionLogger) Error(msg string) {
	l.errorCalls = append(l.errorCalls, exceptionLogCall{msg: msg})
}

func (l *mockExceptionLogger) ErrorCtx(msg string, ctx map[string]interface{}) {
	l.errorCalls = append(l.errorCalls, exceptionLogCall{msg: msg, ctx: ctx})
}

func (l *mockExceptionLogger) Warning(msg string) {
	l.warningCalls = append(l.warningCalls, exceptionLogCall{msg: msg})
}

func (l *mockExceptionLogger) WarningCtx(msg string, ctx map[string]interface{}) {
	l.warningCalls = append(l.warningCalls, exceptionLogCall{msg: msg, ctx: ctx})
}

// TestGetClientIPIgnoresSpoofedProxyHeaders 验证异常处理默认不信任客户端伪造的代理头。
func TestGetClientIPIgnoresSpoofedProxyHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/debug", nil)
	req.RemoteAddr = "198.51.100.10:4567"
	req.Header.Set("X-Forwarded-For", "203.0.113.8")
	req.Header.Set("X-Real-IP", "203.0.113.9")

	if ip := getClientIP(req); ip != "198.51.100.10" {
		t.Fatalf("默认应使用真实连接地址，实际为 %q", ip)
	}
}

// TestReadRequestBodyPreservesBody 验证调试页读取请求体后，后续逻辑仍可继续读取原始 Body。
func TestReadRequestBodyPreservesBody(t *testing.T) {
	rawBody := `{"token":"abc123"}`
	req := httptest.NewRequest(http.MethodPost, "http://example.com/debug", strings.NewReader(rawBody))

	body := readRequestBody(req)
	if body != rawBody {
		t.Fatalf("读取到的请求体不正确，期望 %q，实际 %q", rawBody, body)
	}

	remaining, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("重新读取请求体失败: %v", err)
	}
	if string(remaining) != rawBody {
		t.Fatalf("读取后应恢复请求体，期望 %q，实际 %q", rawBody, string(remaining))
	}
}

// TestRenderBusinessExceptionUsesConfiguredStatus 验证业务异常支持明确的 HTTP 状态码，且不会按系统错误打堆栈。
func TestRenderBusinessExceptionUsesConfiguredStatus(t *testing.T) {
	logger := &mockExceptionLogger{}
	handler := &Handle{
		App: &mockExceptionApp{debug: false},
		Log: logger,
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/orders", nil)
	req.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()

	handler.Render(recorder, req, NewBusinessException(1001, "库存不足").WithStatus(http.StatusConflict))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("业务异常应返回显式配置的 HTTP 状态码 409，实际 %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"code":1001`) {
		t.Fatalf("响应体应保留业务码，实际 %s", recorder.Body.String())
	}
	if len(logger.errorCalls) != 0 {
		t.Fatalf("业务异常不应按系统错误记录堆栈，实际 error 调用 %d 次", len(logger.errorCalls))
	}
	if len(logger.warningCalls) != 1 {
		t.Fatalf("业务异常应按预期异常记录 1 次 warning，实际 %d 次", len(logger.warningCalls))
	}
}

// TestRenderHttpExceptionClientErrorSkipsErrorStack 验证 4xx HTTP 异常不会污染 error 堆栈日志。
func TestRenderHttpExceptionClientErrorSkipsErrorStack(t *testing.T) {
	logger := &mockExceptionLogger{}
	handler := &Handle{
		App: &mockExceptionApp{debug: false},
		Log: logger,
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/missing", nil)
	req.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()

	handler.Render(recorder, req, NewHttpException(http.StatusNotFound, "not found"))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("HTTP 异常应返回自身状态码，实际 %d", recorder.Code)
	}
	if len(logger.errorCalls) != 0 {
		t.Fatalf("4xx HTTP 异常不应记录 error stack，实际 %d 次", len(logger.errorCalls))
	}
	if len(logger.warningCalls) != 1 {
		t.Fatalf("4xx HTTP 异常应记录 1 次 warning，实际 %d 次", len(logger.warningCalls))
	}
}

// TestRenderUnexpectedErrorReportsStack 验证真正的系统错误仍会记录带堆栈的 error 日志。
func TestRenderUnexpectedErrorReportsStack(t *testing.T) {
	logger := &mockExceptionLogger{}
	handler := &Handle{
		App: &mockExceptionApp{debug: false},
		Log: logger,
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/fail", nil)
	req.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()

	handler.Render(recorder, req, errors.New("boom"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("系统错误应返回 500，实际 %d", recorder.Code)
	}
	if len(logger.errorCalls) != 1 {
		t.Fatalf("系统错误应记录 1 次 error，实际 %d 次", len(logger.errorCalls))
	}
	if logger.errorCalls[0].ctx["stack"] == nil {
		t.Fatal("系统错误日志应包含堆栈信息")
	}
}

// TestRenderDebugPageMasksSensitiveData 验证调试异常页会脱敏请求头和请求体中的敏感信息。
func TestRenderDebugPageMasksSensitiveData(t *testing.T) {
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取当前工作目录失败: %v", err)
	}

	handler := &Handle{
		App:    &mockExceptionApp{debug: true},
		TplDir: filepath.Join(workingDir, "tpl"),
	}

	passwordValue := strings.Repeat("12", 3)
	tokenValue := strings.Join([]string{"secret", "token"}, "-")
	refreshValue := strings.Join([]string{"refresh", "value"}, "-")
	cookieValue := "session=" + strings.Join([]string{"abc", "123"}, "")
	formBody := url.Values{
		"password":      []string{passwordValue},
		"token":         []string{tokenValue},
		"refresh_token": []string{refreshValue},
	}.Encode()

	req := httptest.NewRequest(
		http.MethodPost,
		"http://example.com/debug",
		strings.NewReader(formBody),
	)
	req.RemoteAddr = "127.0.0.1:4567"
	req.Header.Set("Authorization", "Bearer "+tokenValue)
	req.Header.Set("Cookie", cookieValue)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()

	handler.Render(recorder, req, errors.New("debug boom"))

	body := recorder.Body.String()
	secrets := []string{tokenValue, passwordValue, refreshValue, strings.TrimPrefix(cookieValue, "session=")}
	for _, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Fatalf("调试异常页不应暴露敏感值 %q，响应内容为 %s", secret, body)
		}
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("调试异常页应展示脱敏占位符，实际 %s", body)
	}
}
