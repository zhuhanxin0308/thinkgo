package exception

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	"thinkgo/framework/context"
)

const redactedPlaceholder = "[REDACTED]"

var sensitiveTextPattern = regexp.MustCompile(`(?i)((?:password|passwd|token|secret|authorization|cookie|session|api[_-]?key|refresh[_-]?token)\s*[:=]\s*)([^&\s,"']+)`)

// AppContract 抽象应用运行时能力，避免异常处理层和框架主对象形成循环依赖。
type AppContract interface {
	IsDebug() bool
}

// Logger 抽象日志能力，便于在异常处理中按级别记录上下文。
type Logger interface {
	Error(msg string)
	ErrorCtx(msg string, ctx map[string]interface{})
	Warning(msg string)
	WarningCtx(msg string, ctx map[string]interface{})
}

// StackFrame 表示一帧结构化堆栈信息。
type StackFrame struct {
	Function  string
	File      string
	ShortFile string
	Line      int
	Source    []SourceLine
}

// SourceLine 表示错误附近的源码行。
type SourceLine struct {
	Number    int
	Content   string
	IsCurrent bool
	IsNear    bool
}

// templateData 是调试异常页所需的模板数据。
type templateData struct {
	ErrorType  string
	Message    string
	ErrorFile  string
	ErrorLine  int
	FrameCount int
	Frames     []StackFrame
	Request    requestData
	Env        envData
}

// requestData 描述请求上下文。
type requestData struct {
	Method  string
	URL     string
	IP      string
	Proto   string
	Body    string
	Headers map[string][]string
}

// envData 描述运行环境信息。
type envData struct {
	GoVersion        string
	FrameworkVersion string
	Mode             string
	OS               string
	Arch             string
}

// Handle 负责把 panic 和业务异常统一转换成 HTTP 响应。
type Handle struct {
	App    AppContract
	Log    Logger
	TplDir string
}

// Render 根据请求类型和运行模式渲染异常响应。
func (h *Handle) Render(w http.ResponseWriter, r *http.Request, err interface{}) {
	switch typed := err.(type) {
	case *HttpException:
		h.reportHTTPException(typed)
		h.renderHttpException(w, r, typed)
		return
	case *ValidateException:
		h.reportValidateException(typed)
		h.renderValidateException(w, r, typed)
		return
	case *BusinessException:
		h.reportBusinessException(typed)
		h.renderBusinessException(w, r, typed)
		return
	default:
		h.Report(err)
	}

	isDebug := h.App != nil && h.App.IsDebug()
	// 仅在调试模式且请求来自回环地址时才暴露堆栈细节，HTML 页与 JSON 响应使用一致门禁，
	// 避免远程攻击者通过 Accept: application/json 绕过限制获取完整堆栈。
	exposeDebug := isDebug && canExposeDebugPage(r)
	if h.isJSONRequest(r) {
		h.renderJSONError(w, http.StatusInternalServerError, err, exposeDebug)
		return
	}
	if exposeDebug {
		h.renderDebugPage(w, r, err)
		return
	}

	context.NewResponse().
		Code(http.StatusInternalServerError).
		Content("Internal Server Error").
		Send(w)
}

// Report 记录真正的系统级异常堆栈。
func (h *Handle) Report(err interface{}) {
	if h.Log == nil {
		return
	}

	h.Log.ErrorCtx(fmt.Sprintf("%v", err), map[string]interface{}{
		"stack": string(debug.Stack()),
	})
}

func (h *Handle) reportHTTPException(err *HttpException) {
	if h.Log == nil {
		return
	}

	ctx := map[string]interface{}{"status": err.StatusCode}
	if err.StatusCode >= http.StatusInternalServerError {
		ctx["stack"] = string(debug.Stack())
		h.Log.ErrorCtx(err.Message, ctx)
		return
	}
	h.Log.WarningCtx(err.Message, ctx)
}

func (h *Handle) reportValidateException(err *ValidateException) {
	if h.Log == nil {
		return
	}

	h.Log.WarningCtx(err.Message, map[string]interface{}{
		"field":  err.Field,
		"status": http.StatusUnprocessableEntity,
	})
}

func (h *Handle) reportBusinessException(err *BusinessException) {
	if h.Log == nil {
		return
	}

	status := err.StatusCode()
	ctx := map[string]interface{}{
		"code":   err.Code,
		"status": status,
	}
	if err.Cause != nil {
		ctx["cause"] = err.Cause.Error()
	}
	if err.Data != nil {
		ctx["data"] = err.Data
	}
	if status >= http.StatusInternalServerError {
		ctx["stack"] = string(debug.Stack())
		h.Log.ErrorCtx(err.Message, ctx)
		return
	}
	h.Log.WarningCtx(err.Message, ctx)
}

// renderHttpException 渲染 HTTP 异常。
func (h *Handle) renderHttpException(w http.ResponseWriter, r *http.Request, err *HttpException) {
	if h.isJSONRequest(r) {
		h.renderJSON(w, err.StatusCode, err.Message, err.Data)
		return
	}

	context.NewResponse().
		Code(err.StatusCode).
		Content(err.Message).
		Send(w)
}

// renderValidateException 渲染参数校验异常。
func (h *Handle) renderValidateException(w http.ResponseWriter, r *http.Request, err *ValidateException) {
	data := map[string]interface{}{
		"field":   err.Field,
		"message": err.Message,
	}

	if h.isJSONRequest(r) {
		h.renderJSON(w, http.StatusUnprocessableEntity, err.Message, data)
		return
	}

	context.NewResponse().
		Code(http.StatusUnprocessableEntity).
		Content(err.Message).
		Send(w)
}

// renderBusinessException 渲染业务异常。
func (h *Handle) renderBusinessException(w http.ResponseWriter, r *http.Request, err *BusinessException) {
	status := err.StatusCode()
	if h.isJSONRequest(r) {
		h.renderJSON(w, status, err.Message, businessPayload(err.Code, err.Data))
		return
	}

	context.NewResponse().
		Code(status).
		Content(err.Message).
		Send(w)
}

// nilWithBusinessCode 保持业务异常 JSON 结构。
func businessPayload(code int, data interface{}) map[string]interface{} {
	return map[string]interface{}{
		"code": code,
		"msg":  "",
		"data": data,
	}
}

// renderDebugPage 渲染调试模式的 HTML 异常页。
func (h *Handle) renderDebugPage(w http.ResponseWriter, r *http.Request, err interface{}) {
	frames := parseGoStack(string(debug.Stack()))
	data := templateData{
		ErrorType:  getErrorType(err),
		Message:    fmt.Sprintf("%v", err),
		FrameCount: len(frames),
		Frames:     frames,
		Request: requestData{
			Method:  r.Method,
			URL:     r.URL.String(),
			IP:      getClientIP(r),
			Proto:   r.Proto,
			Body:    sanitizeRequestBody(readRequestBody(r), r.Header.Get("Content-Type")),
			Headers: sanitizeHeaders(r.Header),
		},
		Env: envData{
			GoVersion:        runtime.Version(),
			FrameworkVersion: "ThinkGo 1.0.0",
			Mode:             "debug",
			OS:               runtime.GOOS,
			Arch:             runtime.GOARCH,
		},
	}

	for _, frame := range frames {
		if isRuntimeFrame(frame.Function) {
			continue
		}
		data.ErrorFile = frame.File
		data.ErrorLine = frame.Line
		break
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)

	tplPath := h.getTemplatePath()
	tpl, errLoad := template.ParseFiles(tplPath)
	if errLoad != nil {
		h.renderFallbackDebug(w, data)
		return
	}
	if errExec := tpl.Execute(w, data); errExec != nil {
		h.renderFallbackDebug(w, data)
	}
}

// renderFallbackDebug 在模板缺失或执行失败时输出降级错误页。
func (h *Handle) renderFallbackDebug(w http.ResponseWriter, data templateData) {
	errorType := template.HTMLEscapeString(data.ErrorType)
	message := template.HTMLEscapeString(data.Message)

	fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Error</title>
<style>body{font-family:sans-serif;margin:40px;background:#1a1d23;color:#abb2bf}
h1{color:#e06c75}pre{background:#282c34;padding:20px;border-radius:8px;overflow:auto;font-size:13px;line-height:1.6}
.info{background:#21252b;padding:16px;border-radius:6px;margin:16px 0}</style></head>
<body><h1>%s</h1><p>%s</p>`, errorType, message)

	if data.ErrorFile != "" {
		fmt.Fprintf(w, `<div class="info">%s:%d</div>`, template.HTMLEscapeString(data.ErrorFile), data.ErrorLine)
	}
	for _, frame := range data.Frames {
		fmt.Fprintf(
			w,
			`<div class="info">%s<br>%s:%d</div>`,
			template.HTMLEscapeString(frame.Function),
			template.HTMLEscapeString(frame.File),
			frame.Line,
		)
	}
	fmt.Fprint(w, `</body></html>`)
}

// getTemplatePath 返回异常模板路径。
func (h *Handle) getTemplatePath() string {
	if h.TplDir != "" {
		path := filepath.Join(h.TplDir, "exception.html")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return filepath.Join("framework", "exception", "tpl", "exception.html")
}

// renderJSONError 渲染通用 JSON 异常响应。
func (h *Handle) renderJSONError(w http.ResponseWriter, code int, err interface{}, isDebug bool) {
	result := map[string]interface{}{
		"code": code,
		"msg":  "Internal Server Error",
		"data": nil,
	}

	if isDebug {
		result["msg"] = fmt.Sprintf("%v", err)
		frames := parseGoStack(string(debug.Stack()))
		traceLines := make([]string, 0, len(frames))
		for _, frame := range frames {
			traceLines = append(traceLines, fmt.Sprintf("%s (%s:%d)", frame.Function, frame.ShortFile, frame.Line))
		}
		result["trace"] = traceLines
	}

	writeJSON(w, code, result)
}

// renderJSON 渲染标准 JSON 响应。
func (h *Handle) renderJSON(w http.ResponseWriter, code int, msg string, data interface{}) {
	if business, ok := data.(map[string]interface{}); ok {
		if _, hasCode := business["code"]; hasCode {
			if currentMsg, exists := business["msg"]; !exists || currentMsg == "" {
				business["msg"] = msg
			}
			writeJSON(w, code, business)
			return
		}
	}

	writeJSON(w, code, map[string]interface{}{
		"code": code,
		"msg":  msg,
		"data": data,
	})
}

// writeJSON 统一写出 JSON 响应。
func writeJSON(w http.ResponseWriter, code int, payload map[string]interface{}) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

// isJSONRequest 判断当前请求是否更适合返回 JSON。
func (h *Handle) isJSONRequest(r *http.Request) bool {
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		return true
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		return true
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		return true
	}
	return strings.HasPrefix(r.URL.Path, "/api/")
}

// getErrorType 返回异常类型名。
func getErrorType(err interface{}) string {
	switch err.(type) {
	case *HttpException:
		return "HttpException"
	case *ValidateException:
		return "ValidateException"
	case *BusinessException:
		return "BusinessException"
	case error:
		return fmt.Sprintf("%T", err)
	default:
		return "PanicError"
	}
}

// getClientIP 返回真实连接地址，默认不信任客户端自带代理头。
func getClientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return strings.Trim(r.RemoteAddr, "[]")
}

// canExposeDebugPage 仅允许本机回环地址访问 HTML 调试页。
// 即使开启了 debug，也必须同时排除本机反代转发的远程客户端。
func canExposeDebugPage(r *http.Request) bool {
	if r == nil {
		return false
	}

	clientIP := getClientIP(r)
	parsedIP := net.ParseIP(clientIP)
	return parsedIP != nil && parsedIP.IsLoopback() && debugProxyHeadersAreLocal(r.Header)
}

// debugProxyHeadersAreLocal 检查代理头中的真实客户端，未知或非回环地址一律不暴露调试页。
func debugProxyHeadersAreLocal(header http.Header) bool {
	candidates := debugProxyAddressCandidates(header)
	if len(candidates) == 0 {
		return true
	}
	for _, candidate := range candidates {
		ip := parseDebugProxyIP(candidate)
		if ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	return true
}

// debugProxyAddressCandidates 提取常见代理头中的客户端地址。
func debugProxyAddressCandidates(header http.Header) []string {
	if len(header) == 0 {
		return nil
	}

	candidates := make([]string, 0)
	for _, value := range header.Values("X-Forwarded-For") {
		candidates = append(candidates, splitDebugProxyList(value)...)
	}
	for _, value := range header.Values("X-Real-IP") {
		candidates = append(candidates, splitDebugProxyList(value)...)
	}
	for _, value := range header.Values("Forwarded") {
		for _, segment := range strings.Split(value, ",") {
			for _, part := range strings.Split(segment, ";") {
				key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
				if ok && strings.EqualFold(strings.TrimSpace(key), "for") {
					candidates = append(candidates, strings.TrimSpace(value))
				}
			}
		}
	}
	return candidates
}

// splitDebugProxyList 拆分代理头中的地址列表。
func splitDebugProxyList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

// parseDebugProxyIP 解析代理头地址，兼容带端口、IPv6 方括号和引号的格式。
func parseDebugProxyIP(value string) net.IP {
	value = strings.TrimSpace(strings.Trim(value, `"`))
	if value == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	return net.ParseIP(value)
}

// readRequestBody 读取调试展示用请求体，并恢复 Body 供后续逻辑继续读取。
func readRequestBody(r *http.Request) string {
	if r.Body == nil {
		return ""
	}

	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(nil))
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(rawBody))

	if len(rawBody) > 4096 {
		rawBody = rawBody[:4096]
	}
	return string(rawBody)
}

func sanitizeHeaders(headers http.Header) map[string][]string {
	if len(headers) == 0 {
		return nil
	}

	sanitized := make(map[string][]string, len(headers))
	for key, values := range headers {
		clonedValues := append([]string(nil), values...)
		if isSensitiveKey(key) {
			for index := range clonedValues {
				clonedValues[index] = redactedPlaceholder
			}
		}
		sanitized[key] = clonedValues
	}
	return sanitized
}

func sanitizeRequestBody(body string, contentType string) string {
	if body == "" {
		return ""
	}

	lowerContentType := strings.ToLower(contentType)
	if strings.Contains(lowerContentType, "application/json") {
		if sanitizedJSON, ok := sanitizeJSONBody(body); ok {
			return sanitizedJSON
		}
	}

	if strings.Contains(lowerContentType, "application/x-www-form-urlencoded") {
		values, err := url.ParseQuery(body)
		if err == nil {
			for key, list := range values {
				if isSensitiveKey(key) {
					for index := range list {
						list[index] = redactedPlaceholder
					}
					values[key] = list
				}
			}
			return values.Encode()
		}
	}

	return sensitiveTextPattern.ReplaceAllString(body, "${1}"+redactedPlaceholder)
}

func sanitizeJSONBody(body string) (string, bool) {
	var payload interface{}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return "", false
	}

	payload = sanitizeJSONValue("", payload)
	sanitizedBody, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return string(sanitizedBody), true
}

func sanitizeJSONValue(key string, value interface{}) interface{} {
	if isSensitiveKey(key) {
		return redactedPlaceholder
	}

	switch typed := value.(type) {
	case map[string]interface{}:
		sanitized := make(map[string]interface{}, len(typed))
		for innerKey, innerValue := range typed {
			sanitized[innerKey] = sanitizeJSONValue(innerKey, innerValue)
		}
		return sanitized
	case []interface{}:
		sanitized := make([]interface{}, len(typed))
		for index, item := range typed {
			sanitized[index] = sanitizeJSONValue(key, item)
		}
		return sanitized
	default:
		return value
	}
}

func isSensitiveKey(key string) bool {
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	if lowerKey == "" {
		return false
	}

	sensitiveKeys := []string{
		"authorization",
		"cookie",
		"set-cookie",
		"password",
		"passwd",
		"token",
		"secret",
		"session",
		"api_key",
		"apikey",
		"access_key",
		"refresh_token",
	}

	for _, sensitiveKey := range sensitiveKeys {
		if lowerKey == sensitiveKey || strings.Contains(lowerKey, sensitiveKey) {
			return true
		}
	}
	return false
}

// parseGoStack 把 Go 堆栈解析为结构化帧信息。
func parseGoStack(stack string) []StackFrame {
	lines := strings.Split(stack, "\n")
	frames := make([]StackFrame, 0)

	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		if line == "" || strings.HasPrefix(line, "goroutine ") {
			continue
		}

		functionName := line
		if left := strings.Index(functionName, "("); left > 0 {
			functionName = functionName[:left]
		}

		if index+1 >= len(lines) {
			continue
		}

		file, lineNumber := parseFileLine(strings.TrimSpace(lines[index+1]))
		if file == "" {
			continue
		}

		frame := StackFrame{
			Function:  functionName,
			File:      file,
			ShortFile: filepath.Base(file),
			Line:      lineNumber,
			Source:    loadSourceContext(file, lineNumber, 8),
		}
		frames = append(frames, frame)
		index++
	}

	return frames
}

// parseFileLine 解析堆栈中的文件位置信息。
func parseFileLine(value string) (string, int) {
	value = strings.TrimSpace(value)
	if index := strings.LastIndex(value, " +0x"); index > 0 {
		value = value[:index]
	}

	for index := len(value) - 1; index >= 0; index-- {
		if value[index] != ':' {
			continue
		}
		lineValue := value[index+1:]
		lineNumber, err := strconv.Atoi(lineValue)
		if err == nil {
			return value[:index], lineNumber
		}
	}

	return value, 0
}

// loadSourceContext 加载错误位置附近的源码上下文。
func loadSourceContext(filePath string, targetLine int, contextLines int) []SourceLine {
	file, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer file.Close()

	startLine := targetLine - contextLines
	if startLine < 1 {
		startLine = 1
	}
	endLine := targetLine + contextLines

	lines := make([]SourceLine, 0, endLine-startLine+1)
	scanner := bufio.NewScanner(file)
	lineNumber := 0

	for scanner.Scan() {
		lineNumber++
		if lineNumber < startLine {
			continue
		}
		if lineNumber > endLine {
			break
		}

		lines = append(lines, SourceLine{
			Number:    lineNumber,
			Content:   scanner.Text(),
			IsCurrent: lineNumber == targetLine,
			IsNear:    lineNumber >= targetLine-2 && lineNumber <= targetLine+2,
		})
	}

	return lines
}

// isRuntimeFrame 判断该帧是否属于运行时或框架内部噪声。
func isRuntimeFrame(functionName string) bool {
	return strings.HasPrefix(functionName, "runtime.") ||
		strings.HasPrefix(functionName, "runtime/debug.") ||
		strings.Contains(functionName, "exception.") ||
		strings.Contains(functionName, "middleware.")
}
