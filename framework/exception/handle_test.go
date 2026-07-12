package exception

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	frameworkVersion "thinkgo/framework/version"
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

type countingReadCloser struct {
	reader io.Reader
	count  int
}

func (r *countingReadCloser) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	r.count += count
	return count, err
}

func (r *countingReadCloser) Close() error {
	return nil
}

type failingExceptionWriter struct {
	header http.Header
	status int
}

func (w *failingExceptionWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failingExceptionWriter) WriteHeader(status int) {
	w.status = status
}

func (w *failingExceptionWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

type shortExceptionWriter struct {
	failingExceptionWriter
}

func (w *shortExceptionWriter) Write(body []byte) (int, error) {
	if len(body) == 0 {
		return 0, nil
	}
	return len(body) - 1, nil
}

type panickingExceptionError struct{}

func (panickingExceptionError) Error() string {
	panic("错误格式化不应击穿异常处理")
}

type panickingJSONValue struct{}

func (panickingJSONValue) MarshalJSON() ([]byte, error) {
	panic("JSON 编码器异常")
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

	if err := handler.Render(recorder, req, NewBusinessException(1001, "库存不足").WithStatus(http.StatusConflict)); err != nil {
		t.Fatalf("渲染业务异常失败: %v", err)
	}

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

	if err := handler.Render(recorder, req, NewHttpException(http.StatusNotFound, "not found")); err != nil {
		t.Fatalf("渲染 HTTP 异常失败: %v", err)
	}

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

	if err := handler.Render(recorder, req, errors.New("boom")); err != nil {
		t.Fatalf("渲染系统异常失败: %v", err)
	}

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

	if err := handler.Render(recorder, req, errors.New("debug boom")); err != nil {
		t.Fatalf("渲染调试异常页失败: %v", err)
	}

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
	if !strings.Contains(body, frameworkVersion.Framework) {
		t.Fatalf("调试异常页应使用中心版本标签 %q，实际 %s", frameworkVersion.Framework, body)
	}
}

// TestRenderRecognizesWrappedExceptions 验证 errors.As 链路中的框架异常仍保留其状态和业务语义。
func TestRenderRecognizesWrappedExceptions(t *testing.T) {
	handler := &Handle{App: &mockExceptionApp{debug: false}}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/orders", nil)
	req.Header.Set("Accept", "application/json")

	httpRecorder := httptest.NewRecorder()
	wrappedHTTP := fmt.Errorf("查询失败: %w", NewHttpException(http.StatusNotFound, "订单不存在"))
	if err := handler.Render(httpRecorder, req, wrappedHTTP); err != nil {
		t.Fatalf("渲染包装 HTTP 异常失败: %v", err)
	}
	if httpRecorder.Code != http.StatusNotFound || !strings.Contains(httpRecorder.Body.String(), "订单不存在") {
		t.Fatalf("包装 HTTP 异常语义丢失，状态=%d，响应=%s", httpRecorder.Code, httpRecorder.Body.String())
	}

	businessRecorder := httptest.NewRecorder()
	wrappedBusiness := fmt.Errorf("提交失败: %w", NewBusinessException(4201, "库存不足").WithStatus(http.StatusConflict))
	if err := handler.Render(businessRecorder, req, wrappedBusiness); err != nil {
		t.Fatalf("渲染包装业务异常失败: %v", err)
	}
	if businessRecorder.Code != http.StatusConflict || !strings.Contains(businessRecorder.Body.String(), `"code":4201`) {
		t.Fatalf("包装业务异常语义丢失，状态=%d，响应=%s", businessRecorder.Code, businessRecorder.Body.String())
	}
}

// TestRenderMasksServerExceptionDetails 验证生产环境不会返回 5xx 异常的内部消息和附加数据。
func TestRenderMasksServerExceptionDetails(t *testing.T) {
	handler := &Handle{App: &mockExceptionApp{debug: false}}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/fail", nil)
	req.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()
	secret := "database-password-123"

	serverError := NewHttpException(http.StatusInternalServerError, "数据库连接失败: "+secret).
		WithData(map[string]interface{}{"secret": secret})
	if err := handler.Render(recorder, req, serverError); err != nil {
		t.Fatalf("渲染服务端异常失败: %v", err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("服务端异常状态错误: %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), secret) || strings.Contains(recorder.Body.String(), "数据库连接失败") {
		t.Fatalf("生产响应泄露内部异常详情: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Internal Server Error") {
		t.Fatalf("生产响应应使用通用错误消息: %s", recorder.Body.String())
	}
}

// TestRenderRejectsInvalidExceptionStatus 验证异常不能利用非法或成功状态码构造伪成功响应。
func TestRenderRejectsInvalidExceptionStatus(t *testing.T) {
	handler := &Handle{App: &mockExceptionApp{debug: false}}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/fail", nil)
	req.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()

	if err := handler.Render(recorder, req, &HttpException{StatusCode: http.StatusOK, Message: "伪成功"}); err == nil {
		t.Fatal("非法异常状态应返回可诊断错误")
	}
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "伪成功") {
		t.Fatalf("非法异常状态必须安全降级为 500，状态=%d，响应=%s", recorder.Code, recorder.Body.String())
	}
}

// TestJSONRequestNegotiationHonorsQuality 验证 JSON 媒体类型、供应商类型和 q=0 按 HTTP 权重协商。
func TestJSONRequestNegotiationHonorsQuality(t *testing.T) {
	handler := &Handle{}
	tests := []struct {
		name        string
		accept      string
		contentType string
		path        string
		expected    bool
	}{
		{name: "显式拒绝 JSON", accept: "application/json;q=0, text/html;q=1", path: "/web", expected: false},
		{name: "拒绝非有限权重", accept: "application/json;q=NaN", path: "/web", expected: false},
		{name: "HTML 权重更高", accept: "application/json;q=0.4, text/html;q=0.9", path: "/web", expected: false},
		{name: "供应商 JSON 权重更高", accept: "application/problem+json;q=0.8, text/html;q=0.2", path: "/web", expected: true},
		{name: "JSON 请求体回退", contentType: "application/problem+json; charset=utf-8", path: "/web", expected: true},
		{name: "API 根路径", path: "/api", expected: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com"+test.path, nil)
			req.Header.Set("Accept", test.accept)
			req.Header.Set("Content-Type", test.contentType)
			if actual := handler.isJSONRequest(req); actual != test.expected {
				t.Fatalf("JSON 协商结果错误，期望 %t，实际 %t", test.expected, actual)
			}
		})
	}
	if handler.isJSONRequest(nil) {
		t.Fatal("空请求不应触发 JSON 协商")
	}
}

// TestReadRequestBodyIsBoundedAndPreservesStream 验证调试快照只预读有限前缀且不破坏后续完整读取。
func TestReadRequestBodyIsBoundedAndPreservesStream(t *testing.T) {
	rawBody := strings.Repeat("x", 1<<20)
	reader := &countingReadCloser{reader: strings.NewReader(rawBody)}
	req := httptest.NewRequest(http.MethodPost, "http://example.com/debug", nil)
	req.Body = reader

	snapshot := readRequestBody(req)
	if reader.count > 4097 {
		t.Fatalf("调试快照读取无界，实际预读 %d 字节", reader.count)
	}
	if len(snapshot) > 4200 || !strings.Contains(snapshot, "TRUNCATED") {
		t.Fatalf("超长请求体快照应明确截断，长度=%d，内容后缀=%q", len(snapshot), snapshot[maxInt(0, len(snapshot)-32):])
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("读取恢复后的请求体失败: %v", err)
	}
	if string(restored) != rawBody {
		t.Fatalf("恢复后的请求体不完整，期望 %d 字节，实际 %d 字节", len(rawBody), len(restored))
	}
}

// TestRenderDebugPageOmitsBinaryBodyAndRedactsURL 验证调试页不会展示二进制体或 URL 中的敏感查询参数。
func TestRenderDebugPageOmitsBinaryBodyAndRedactsURL(t *testing.T) {
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取工作目录失败: %v", err)
	}
	handler := &Handle{App: &mockExceptionApp{debug: true}, TplDir: filepath.Join(workingDir, "tpl")}
	secret := strings.Join([]string{"query", "and", "binary", "secret"}, "-")
	req := httptest.NewRequest(http.MethodPost, "http://example.com/debug?token="+secret+"&page=1", strings.NewReader(secret))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/octet-stream")
	recorder := httptest.NewRecorder()

	if err := handler.Render(recorder, req, errors.New("debug boom")); err != nil {
		t.Fatalf("渲染调试异常页失败: %v", err)
	}
	if strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("调试页泄露 URL 或二进制体中的敏感值: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), url.QueryEscape(redactedPlaceholder)) {
		t.Fatalf("调试页应标记已脱敏查询参数: %s", recorder.Body.String())
	}
}

// TestRenderJSONDoesNotMutateCallerPayload 验证构建异常信封不会回写调用方提供的 map。
func TestRenderJSONDoesNotMutateCallerPayload(t *testing.T) {
	handler := &Handle{}
	payload := map[string]interface{}{"code": 7001, "msg": "", "data": map[string]interface{}{"id": 1}}
	recorder := httptest.NewRecorder()

	if err := handler.renderJSON(recorder, http.StatusBadRequest, "请求失败", payload); err != nil {
		t.Fatalf("渲染 JSON 异常失败: %v", err)
	}
	if payload["msg"] != "" {
		t.Fatalf("异常渲染不应修改调用方数据，实际为 %#v", payload)
	}
}

// TestRenderReturnsWriteError 验证底层连接写失败会返回给 HTTP 内核记录，而不是被静默吞掉。
func TestRenderReturnsWriteError(t *testing.T) {
	handler := &Handle{App: &mockExceptionApp{debug: false}}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/fail", nil)
	writer := &failingExceptionWriter{}

	if err := handler.Render(writer, req, errors.New("boom")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("异常响应应返回底层写错误，实际为 %v", err)
	}
}

// TestRenderSurvivesTypedNilAndPanickingError 验证恶意或异常 Error 实现不会再次击穿恢复链路。
func TestRenderSurvivesTypedNilAndPanickingError(t *testing.T) {
	handler := &Handle{App: &mockExceptionApp{debug: false}}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/fail", nil)

	var typedNil *HttpException
	values := []interface{}{error(typedNil), panickingExceptionError{}}
	for _, value := range values {
		recorder := httptest.NewRecorder()
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("异常处理发生二次 panic: %v", recovered)
				}
			}()
			_ = handler.Render(recorder, req, value)
		}()
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("异常格式化失败应返回 500，实际为 %d", recorder.Code)
		}
	}
}

// TestRenderValidateExceptionNegotiatesJSONAndText 验证校验异常的两种表现形式、日志级别和安全响应头。
func TestRenderValidateExceptionNegotiatesJSONAndText(t *testing.T) {
	logger := &mockExceptionLogger{}
	handler := &Handle{App: &mockExceptionApp{debug: false}, Log: logger}
	validation := NewValidateException("邮箱格式错误", "email")

	jsonRequest := httptest.NewRequest(http.MethodPost, "http://example.com/users", nil)
	jsonRequest.Header.Set("Accept", "application/json")
	jsonRecorder := httptest.NewRecorder()
	if err := handler.Render(jsonRecorder, jsonRequest, validation); err != nil {
		t.Fatalf("渲染 JSON 校验异常失败: %v", err)
	}
	if jsonRecorder.Code != http.StatusUnprocessableEntity || !strings.Contains(jsonRecorder.Body.String(), `"field":"email"`) {
		t.Fatalf("JSON 校验异常错误，状态=%d，响应=%s", jsonRecorder.Code, jsonRecorder.Body.String())
	}

	textRequest := httptest.NewRequest(http.MethodPost, "http://example.com/users", nil)
	textRequest.Header.Set("Accept", "text/html")
	textRecorder := httptest.NewRecorder()
	if err := handler.Render(textRecorder, textRequest, validation); err != nil {
		t.Fatalf("渲染文本校验异常失败: %v", err)
	}
	if textRecorder.Code != http.StatusUnprocessableEntity || textRecorder.Body.String() != "邮箱格式错误" {
		t.Fatalf("文本校验异常错误，状态=%d，响应=%q", textRecorder.Code, textRecorder.Body.String())
	}
	if textRecorder.Header().Get("X-Content-Type-Options") != "nosniff" || textRecorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("异常响应缺少安全响应头: %#v", textRecorder.Header())
	}
	if len(logger.warningCalls) != 2 || len(logger.errorCalls) != 0 {
		t.Fatalf("校验异常日志级别错误: warning=%d error=%d", len(logger.warningCalls), len(logger.errorCalls))
	}
}

// TestRenderMasksBusinessServerFailure 验证 5xx 业务异常的业务码、消息和数据在生产环境统一隐藏。
func TestRenderMasksBusinessServerFailure(t *testing.T) {
	handler := &Handle{App: &mockExceptionApp{debug: false}}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/orders", nil)
	req.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()
	secret := strings.Join([]string{"internal", "inventory", "secret"}, "-")
	exception := NewBusinessException(9001, "库存服务故障: "+secret).
		WithStatus(http.StatusServiceUnavailable).
		WithData(map[string]interface{}{"secret": secret})

	if err := handler.Render(recorder, req, exception); err != nil {
		t.Fatalf("渲染服务端业务异常失败: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), secret) || strings.Contains(recorder.Body.String(), `"code":9001`) {
		t.Fatalf("服务端业务异常未正确脱敏，状态=%d，响应=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":503`) || !strings.Contains(recorder.Body.String(), internalServerErrorText) {
		t.Fatalf("服务端业务异常应使用统一信封: %s", recorder.Body.String())
	}
}

// TestRenderLocalDebugJSONIncludesTrace 验证仅本机调试请求能在 JSON 中获得错误消息和结构化堆栈。
func TestRenderLocalDebugJSONIncludesTrace(t *testing.T) {
	handler := &Handle{App: &mockExceptionApp{debug: true}}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/debug", nil)
	req.RemoteAddr = "127.0.0.1:4567"
	req.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()

	if err := handler.Render(recorder, req, errors.New("local debug detail")); err != nil {
		t.Fatalf("渲染本机调试 JSON 失败: %v", err)
	}
	if !strings.Contains(recorder.Body.String(), "local debug detail") || !strings.Contains(recorder.Body.String(), `"trace"`) {
		t.Fatalf("本机调试 JSON 缺少详情或堆栈: %s", recorder.Body.String())
	}
}

// TestExceptionSanitizersHandleStructuredInputs 验证 JSON、表单、响应头和降级 HTML 中的敏感值均被安全处理。
func TestExceptionSanitizersHandleStructuredInputs(t *testing.T) {
	jsonBody := `{"user":{"password":"secret"},"items":[{"token":"abc"}],"name":"safe"}`
	sanitizedJSON, ok := sanitizeJSONBody(jsonBody)
	if !ok || strings.Contains(sanitizedJSON, "secret") || strings.Contains(sanitizedJSON, `"abc"`) || !strings.Contains(sanitizedJSON, redactedPlaceholder) {
		t.Fatalf("JSON 脱敏错误: %q, ok=%t", sanitizedJSON, ok)
	}
	form := sanitizeRequestBody("password=secret&name=safe", "application/x-www-form-urlencoded")
	if strings.Contains(form, "secret") || !strings.Contains(form, url.QueryEscape(redactedPlaceholder)) {
		t.Fatalf("表单脱敏错误: %q", form)
	}
	headers := sanitizeHeaders(http.Header{"Authorization": {"Bearer secret"}, "X-Trace": {"line\r\nbreak"}})
	if headers["Authorization"][0] != redactedPlaceholder || strings.ContainsAny(headers["X-Trace"][0], "\r\n") {
		t.Fatalf("请求头脱敏错误: %#v", headers)
	}

	var fallback strings.Builder
	renderFallbackDebug(&fallback, templateData{ErrorType: `<type>`, Message: `<script>`, ErrorFile: `file.go`, ErrorLine: 10})
	if strings.Contains(fallback.String(), `<script>`) || !strings.Contains(fallback.String(), `&lt;script&gt;`) {
		t.Fatalf("降级调试页未转义动态内容: %s", fallback.String())
	}
}

// TestWriteJSONFailsClosedAndDetectsShortWrite 验证序列化失败、非法状态和静默短写都不会被误判为成功。
func TestWriteJSONFailsClosedAndDetectsShortWrite(t *testing.T) {
	recorder := httptest.NewRecorder()
	marshalErr := writeJSON(recorder, http.StatusBadRequest, map[string]interface{}{"data": make(chan int)})
	if marshalErr == nil || recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), internalServerErrorText) {
		t.Fatalf("JSON 序列化失败应安全降级，状态=%d，响应=%s，错误=%v", recorder.Code, recorder.Body.String(), marshalErr)
	}
	panicRecorder := httptest.NewRecorder()
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("JSON MarshalJSON panic 不得击穿异常渲染: %v", recovered)
			}
		}()
		if err := writeJSON(panicRecorder, http.StatusBadRequest, map[string]interface{}{"data": panickingJSONValue{}}); err == nil {
			t.Fatal("JSON 编码 panic 应转换为可诊断错误")
		}
	}()
	if panicRecorder.Code != http.StatusInternalServerError || !strings.Contains(panicRecorder.Body.String(), internalServerErrorText) {
		t.Fatalf("JSON 编码 panic 应安全降级为 500: status=%d body=%s", panicRecorder.Code, panicRecorder.Body.String())
	}

	invalidRecorder := httptest.NewRecorder()
	if err := writeJSON(invalidRecorder, http.StatusOK, map[string]interface{}{"ok": true}); !errors.Is(err, ErrInvalidExceptionStatus) {
		t.Fatalf("异常 JSON 不得使用成功状态，实际为 %v", err)
	}
	if invalidRecorder.Code != http.StatusInternalServerError {
		t.Fatalf("非法异常状态应降级为 500，实际为 %d", invalidRecorder.Code)
	}

	shortWriter := &shortExceptionWriter{}
	if err := writeTextResponse(shortWriter, http.StatusInternalServerError, "complete"); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("异常响应短写应返回 io.ErrShortWrite，实际为 %v", err)
	}
}

// TestKnownExceptionLogsEscapeControlCharacters 验证预期异常日志不会被消息中的换行伪造额外日志记录。
func TestKnownExceptionLogsEscapeControlCharacters(t *testing.T) {
	logger := &mockExceptionLogger{}
	handler := &Handle{App: &mockExceptionApp{debug: false}, Log: logger}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/missing", nil)
	req.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()
	if err := handler.Render(recorder, req, NewHttpException(http.StatusNotFound, "missing\r\ninjected")); err != nil {
		t.Fatalf("渲染 HTTP 异常失败: %v", err)
	}
	if len(logger.warningCalls) != 1 || strings.ContainsAny(logger.warningCalls[0].msg, "\r\n") {
		t.Fatalf("异常日志消息未转义控制字符: %#v", logger.warningCalls)
	}
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
