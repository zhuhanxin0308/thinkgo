package exception

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	frameworkContext "github.com/zhuhanxin0308/thinkgo/framework/context"
	frameworkLog "github.com/zhuhanxin0308/thinkgo/framework/log"
	frameworkVersion "github.com/zhuhanxin0308/thinkgo/framework/version"
)

const (
	redactedPlaceholder        = "[REDACTED]"
	internalServerErrorText    = "Internal Server Error"
	maxDebugRequestBodyBytes   = 4096
	maxExceptionLogTextBytes   = 4096
	maxAcceptHeaderBytes       = 8192
	maxAcceptMediaRangeCount   = 64
	omittedBinaryBodyText      = "[BINARY BODY OMITTED]"
	truncatedRequestBodySuffix = "\n[TRUNCATED]"
)

var (
	// ErrInvalidExceptionWriter 表示异常响应缺少可用的底层写入器。
	ErrInvalidExceptionWriter = errors.New("异常响应写入器无效")
	// ErrInvalidExceptionStatus 表示异常错误地使用了非 4xx/5xx 状态码。
	ErrInvalidExceptionStatus = errors.New("异常 HTTP 状态码无效")
)

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

// Handler 定义应用可以通过 provider 替换的异常报告与渲染契约。
// HTTP 生命周期会先调用 Report，再调用 Render，与 ThinkPHP Handle 调用顺序一致。
type Handler interface {
	Report(recovered interface{})
	Render(writer http.ResponseWriter, request *http.Request, recovered interface{}) error
}

// Handle 负责把 panic 和业务异常统一转换成 HTTP 响应。
type Handle struct {
	App    AppContract
	Log    Logger
	TplDir string
}

// Render 根据异常链、请求协商和运行模式渲染响应，并把底层写入错误返回给调用方。
func (h *Handle) Render(w http.ResponseWriter, r *http.Request, recovered interface{}) error {
	if isNilResponseWriter(w) {
		return ErrInvalidExceptionWriter
	}

	exposeDetails := h != nil && h.App != nil && h.App.IsDebug() && canExposeDebugPage(r)
	if business := asBusinessException(recovered); business != nil {
		statusErr := validateKnownExceptionStatus(business.HTTPStatus)
		if statusErr != nil {
			h.Report(statusErr)
			return errors.Join(statusErr, h.renderInternalServerError(w, r))
		}
		return h.renderBusinessException(w, r, business, exposeDetails)
	}
	if validation := asValidateException(recovered); validation != nil {
		return h.renderValidateException(w, r, validation)
	}
	if httpException := asHTTPException(recovered); httpException != nil {
		statusErr := validateKnownExceptionStatus(httpException.StatusCode)
		if statusErr != nil {
			h.Report(statusErr)
			return errors.Join(statusErr, h.renderInternalServerError(w, r))
		}
		return h.renderHttpException(w, r, httpException, exposeDetails)
	}

	if h.isJSONRequest(r) {
		return h.renderJSONError(w, http.StatusInternalServerError, recovered, exposeDetails)
	}
	if exposeDetails {
		return h.renderDebugPage(w, r, recovered)
	}
	return writeTextResponse(w, http.StatusInternalServerError, internalServerErrorText)
}

func asBusinessException(value interface{}) *BusinessException {
	err, ok := value.(error)
	if !ok || isNilValue(value) {
		return nil
	}
	var target *BusinessException
	if safeErrorsAs(err, &target) {
		return target
	}
	return nil
}

func asValidateException(value interface{}) *ValidateException {
	err, ok := value.(error)
	if !ok || isNilValue(value) {
		return nil
	}
	var target *ValidateException
	if safeErrorsAs(err, &target) {
		return target
	}
	return nil
}

func asHTTPException(value interface{}) *HttpException {
	err, ok := value.(error)
	if !ok || isNilValue(value) {
		return nil
	}
	var target *HttpException
	if safeErrorsAs(err, &target) {
		return target
	}
	return nil
}

func safeErrorsAs(err error, target interface{}) (matched bool) {
	defer func() {
		if recover() != nil {
			matched = false
		}
	}()
	return errors.As(err, target) && !isNilValue(reflect.ValueOf(target).Elem().Interface())
}

func validateKnownExceptionStatus(status int) error {
	if validExceptionStatus(status) {
		return nil
	}
	return fmt.Errorf("%w: %d", ErrInvalidExceptionStatus, status)
}

func (h *Handle) renderInternalServerError(w http.ResponseWriter, r *http.Request) error {
	if h.isJSONRequest(r) {
		return writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"code": http.StatusInternalServerError,
			"msg":  internalServerErrorText,
			"data": nil,
		})
	}
	return writeTextResponse(w, http.StatusInternalServerError, internalServerErrorText)
}

func isNilResponseWriter(writer http.ResponseWriter) bool {
	return writer == nil || isNilValue(writer)
}

func isNilValue(value interface{}) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func safeExceptionTextWith(value interface{}, sanitize func(string) string) (text string) {
	typeName := fmt.Sprintf("%T", value)
	if value == nil || isNilValue(value) {
		return typeName
	}
	text = typeName
	defer func() {
		if recover() != nil {
			text = typeName
		}
		text = sanitize(text)
	}()

	switch typed := value.(type) {
	case string:
		text = typed
	case []byte:
		text = string(typed)
	case error:
		text = typed.Error()
	default:
		reflected := reflect.ValueOf(value)
		switch reflected.Kind() {
		case reflect.Bool:
			text = strconv.FormatBool(reflected.Bool())
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			text = strconv.FormatInt(reflected.Int(), 10)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			text = strconv.FormatUint(reflected.Uint(), 10)
		case reflect.Float32, reflect.Float64:
			text = strconv.FormatFloat(reflected.Float(), 'g', -1, reflected.Type().Bits())
		}
	}
	return text
}

func sanitizeExceptionText(text string) string {
	return normalizeExceptionText(frameworkLog.SanitizeErrorTextFully(text))
}

// normalizeExceptionText 统一处理异常文本的长度、编码和控制字符，避免结构化脱敏后的占位符再次被改写。
func normalizeExceptionText(text string) string {
	if len(text) > maxExceptionLogTextBytes {
		text = strings.ToValidUTF8(text[:maxExceptionLogTextBytes], "�") + "…"
	} else {
		text = strings.ToValidUTF8(text, "�")
	}
	text = strings.NewReplacer("\r", `\r`, "\n", `\n`, "\t", `\t`).Replace(text)
	return strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, text)
}

// Report 按异常类型选择日志级别，并与 Render 保持彼此独立。
func (h *Handle) Report(err interface{}) {
	if h == nil || h.Log == nil {
		return
	}
	if business := asBusinessException(err); business != nil {
		h.reportBusinessException(business)
		return
	}
	if validation := asValidateException(err); validation != nil {
		h.reportValidateException(validation)
		return
	}
	if httpException := asHTTPException(err); httpException != nil {
		h.reportHTTPException(httpException)
		return
	}

	h.Log.ErrorCtx(safeExceptionLogText(err), map[string]interface{}{
		"stack": string(debug.Stack()),
	})
}

func (h *Handle) reportHTTPException(err *HttpException) {
	if h == nil || h.Log == nil || err == nil {
		return
	}

	ctx := map[string]interface{}{"status": err.StatusCode}
	if err.StatusCode >= http.StatusInternalServerError {
		ctx["stack"] = string(debug.Stack())
		h.Log.ErrorCtx(sanitizeExceptionLogText(err.Message), ctx)
		return
	}
	h.Log.WarningCtx(sanitizeExceptionLogText(err.Message), ctx)
}

func (h *Handle) reportValidateException(err *ValidateException) {
	if h == nil || h.Log == nil || err == nil {
		return
	}

	h.Log.WarningCtx(sanitizeExceptionLogText(err.Message), map[string]interface{}{
		"field":  err.Field,
		"status": http.StatusUnprocessableEntity,
	})
}

func (h *Handle) reportBusinessException(err *BusinessException) {
	if h == nil || h.Log == nil || err == nil {
		return
	}

	status := err.StatusCode()
	ctx := map[string]interface{}{
		"code":   err.Code,
		"status": status,
	}
	if err.Cause != nil {
		ctx["cause"] = safeExceptionLogText(err.Cause)
	}
	if err.Data != nil {
		ctx["data"] = cloneExceptionData(err.Data)
	}
	if status >= http.StatusInternalServerError {
		ctx["stack"] = string(debug.Stack())
		h.Log.ErrorCtx(sanitizeExceptionLogText(err.Message), ctx)
		return
	}
	h.Log.WarningCtx(sanitizeExceptionLogText(err.Message), ctx)
}

// renderHttpException 渲染 HTTP 异常，并在非本地调试场景隐藏 5xx 详情。
func (h *Handle) renderHttpException(w http.ResponseWriter, r *http.Request, err *HttpException, exposeDetails bool) error {
	message := err.Message
	data := cloneExceptionData(err.Data)
	if err.StatusCode >= http.StatusInternalServerError && !exposeDetails {
		message = internalServerErrorText
		data = nil
	}
	if h.isJSONRequest(r) {
		return h.renderJSON(w, err.StatusCode, message, data)
	}
	return writeTextResponse(w, err.StatusCode, message)
}

// renderValidateException 渲染参数校验异常。
func (h *Handle) renderValidateException(w http.ResponseWriter, r *http.Request, err *ValidateException) error {
	data := map[string]interface{}{
		"field":   err.Field,
		"message": err.Message,
	}

	if h.isJSONRequest(r) {
		return h.renderJSON(w, http.StatusUnprocessableEntity, err.Message, data)
	}
	return writeTextResponse(w, http.StatusUnprocessableEntity, err.Message)
}

// renderBusinessException 渲染业务异常，服务端失败只返回统一错误信封。
func (h *Handle) renderBusinessException(w http.ResponseWriter, r *http.Request, err *BusinessException, exposeDetails bool) error {
	status := err.StatusCode()
	message := err.Message
	code := err.Code
	data := cloneExceptionData(err.Data)
	if status >= http.StatusInternalServerError && !exposeDetails {
		message = internalServerErrorText
		code = status
		data = nil
	}
	if h.isJSONRequest(r) {
		return writeJSON(w, status, map[string]interface{}{
			"code": code,
			"msg":  message,
			"data": data,
		})
	}
	return writeTextResponse(w, status, message)
}

// renderDebugPage 先在内存中完整渲染本机调试页，避免模板失败后拼接两份半截响应。
func (h *Handle) renderDebugPage(w http.ResponseWriter, r *http.Request, err interface{}) error {
	if r == nil {
		return h.renderInternalServerError(w, nil)
	}
	frames := parseGoStack(string(debug.Stack()))
	data := templateData{
		ErrorType:  getErrorType(err),
		Message:    safeExceptionText(err),
		FrameCount: len(frames),
		Frames:     frames,
		Request: requestData{
			Method:  r.Method,
			URL:     sanitizeRequestURL(r),
			IP:      getClientIP(r),
			Proto:   r.Proto,
			Body:    debugRequestBody(r),
			Headers: sanitizeHeaders(r.Header),
		},
		Env: envData{
			GoVersion:        runtime.Version(),
			FrameworkVersion: frameworkVersion.Framework,
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

	var output bytes.Buffer
	tplPath := h.getTemplatePath()
	tpl, errLoad := template.ParseFiles(tplPath)
	if errLoad != nil {
		h.Report(errLoad)
		renderFallbackDebug(&output, data)
	} else if errExec := tpl.Execute(&output, data); errExec != nil {
		h.Report(errExec)
		output.Reset()
		renderFallbackDebug(&output, data)
	}
	return writeHTMLResponse(w, http.StatusInternalServerError, output.Bytes())
}

// renderFallbackDebug 在模板缺失或执行失败时输出降级错误页。
func renderFallbackDebug(w io.Writer, data templateData) {
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
func (h *Handle) renderJSONError(w http.ResponseWriter, code int, err interface{}, isDebug bool) error {
	result := map[string]interface{}{
		"code": code,
		"msg":  internalServerErrorText,
		"data": nil,
	}

	if isDebug {
		result["msg"] = safeExceptionText(err)
		frames := parseGoStack(string(debug.Stack()))
		traceLines := make([]string, 0, len(frames))
		for _, frame := range frames {
			traceLines = append(traceLines, fmt.Sprintf("%s (%s:%d)", frame.Function, frame.ShortFile, frame.Line))
		}
		result["trace"] = traceLines
	}

	return writeJSON(w, code, result)
}

// renderJSON 渲染标准 JSON 响应。
func (h *Handle) renderJSON(w http.ResponseWriter, code int, msg string, data interface{}) error {
	return writeJSON(w, code, map[string]interface{}{
		"code": code,
		"msg":  msg,
		"data": data,
	})
}

// writeJSON 在提交响应头前完成序列化，失败时安全降级为固定 500 信封。
func writeJSON(w http.ResponseWriter, code int, payload map[string]interface{}) error {
	if isNilResponseWriter(w) {
		return ErrInvalidExceptionWriter
	}
	var resultErr error
	if !validExceptionStatus(code) {
		resultErr = fmt.Errorf("%w: %d", ErrInvalidExceptionStatus, code)
		code = http.StatusInternalServerError
		payload = nil
	}
	body, marshalErr := marshalExceptionJSON(payload)
	if marshalErr != nil || payload == nil {
		resultErr = errors.Join(resultErr, marshalErr)
		code = http.StatusInternalServerError
		body = []byte(`{"code":500,"msg":"Internal Server Error","data":null}`)
	}
	return errors.Join(resultErr, writeEncodedResponse(w, code, "application/json; charset=utf-8", body))
}

func marshalExceptionJSON(payload interface{}) (body []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			body = nil
			err = fmt.Errorf("序列化异常 JSON 时发生 panic: %s", safeExceptionText(recovered))
		}
	}()
	return json.Marshal(frameworkContext.NormalizeJSONTimes(payload))
}

func writeTextResponse(w http.ResponseWriter, code int, message string) error {
	return writeEncodedResponse(w, code, "text/plain; charset=utf-8", []byte(message))
}

func writeHTMLResponse(w http.ResponseWriter, code int, body []byte) error {
	return writeEncodedResponse(w, code, "text/html; charset=utf-8", body)
}

func writeEncodedResponse(w http.ResponseWriter, code int, contentType string, body []byte) error {
	if isNilResponseWriter(w) {
		return ErrInvalidExceptionWriter
	}
	if !validExceptionStatus(code) {
		return fmt.Errorf("%w: %d", ErrInvalidExceptionStatus, code)
	}
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "no-store")
	header.Del("Content-Length")
	w.WriteHeader(code)
	written, err := w.Write(body)
	if err == nil && written != len(body) {
		return io.ErrShortWrite
	}
	return err
}

type mediaPreference struct {
	set         bool
	quality     float64
	specificity int
	order       int
}

// isJSONRequest 按 Accept 权重优先协商，并兼容 API、XHR 和 JSON 请求体来源。
func (h *Handle) isJSONRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	accept := strings.TrimSpace(r.Header.Get("Accept"))
	if accept != "" {
		jsonPreference, htmlPreference, recognized := negotiateExceptionMedia(accept)
		if recognized {
			return preferJSON(jsonPreference, htmlPreference)
		}
		return false
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Requested-With")), "XMLHttpRequest") {
		return true
	}
	if r.URL != nil && (r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/")) {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && isJSONMediaType(mediaType)
}

func negotiateExceptionMedia(header string) (mediaPreference, mediaPreference, bool) {
	var jsonPreference mediaPreference
	var htmlPreference mediaPreference
	if len(header) > maxAcceptHeaderBytes {
		return jsonPreference, htmlPreference, false
	}
	parts := strings.Split(header, ",")
	if len(parts) > maxAcceptMediaRangeCount {
		return jsonPreference, htmlPreference, false
	}
	for order, part := range parts {
		mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		quality := 1.0
		if rawQuality, exists := parameters["q"]; exists {
			quality, err = strconv.ParseFloat(strings.TrimSpace(rawQuality), 64)
			if err != nil || math.IsNaN(quality) || math.IsInf(quality, 0) || quality < 0 || quality > 1 {
				continue
			}
		}
		mediaType = strings.ToLower(mediaType)
		if specificity, matches := jsonMediaSpecificity(mediaType); matches {
			updateMediaPreference(&jsonPreference, quality, specificity, order)
		}
		if specificity, matches := htmlMediaSpecificity(mediaType); matches {
			updateMediaPreference(&htmlPreference, quality, specificity, order)
		}
	}
	return jsonPreference, htmlPreference, jsonPreference.set || htmlPreference.set
}

func updateMediaPreference(preference *mediaPreference, quality float64, specificity, order int) {
	if !preference.set || specificity > preference.specificity ||
		(specificity == preference.specificity && quality > preference.quality) {
		*preference = mediaPreference{set: true, quality: quality, specificity: specificity, order: order}
	}
}

func preferJSON(jsonPreference, htmlPreference mediaPreference) bool {
	if !jsonPreference.set || jsonPreference.quality <= 0 {
		return false
	}
	if !htmlPreference.set || htmlPreference.quality <= 0 {
		return true
	}
	if jsonPreference.quality != htmlPreference.quality {
		return jsonPreference.quality > htmlPreference.quality
	}
	if jsonPreference.specificity != htmlPreference.specificity {
		return jsonPreference.specificity > htmlPreference.specificity
	}
	if jsonPreference.order != htmlPreference.order {
		return jsonPreference.order < htmlPreference.order
	}
	return false
}

func jsonMediaSpecificity(mediaType string) (int, bool) {
	if isJSONMediaType(mediaType) {
		return 2, true
	}
	if mediaType == "application/*" {
		return 1, true
	}
	return 0, mediaType == "*/*"
}

func htmlMediaSpecificity(mediaType string) (int, bool) {
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return 2, true
	}
	if mediaType == "text/*" {
		return 1, true
	}
	return 0, mediaType == "*/*"
}

func isJSONMediaType(mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// getErrorType 返回异常类型名。
func getErrorType(err interface{}) string {
	if asBusinessException(err) != nil {
		return "BusinessException"
	}
	if asValidateException(err) != nil {
		return "ValidateException"
	}
	if asHTTPException(err) != nil {
		return "HttpException"
	}
	if _, ok := err.(error); ok {
		return fmt.Sprintf("%T", err)
	}
	return "PanicError"
}

// getClientIP 返回真实连接地址，默认不信任客户端自带代理头。
func getClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
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
	if r == nil || r.Body == nil {
		return ""
	}

	original := r.Body
	prefix, err := io.ReadAll(io.LimitReader(original, maxDebugRequestBodyBytes+1))
	r.Body = &replayedRequestBody{
		Reader: io.MultiReader(bytes.NewReader(prefix), original),
		Closer: original,
	}
	if err != nil {
		return ""
	}
	if len(prefix) > maxDebugRequestBodyBytes {
		return string(prefix[:maxDebugRequestBodyBytes]) + truncatedRequestBodySuffix
	}
	return string(prefix)
}

type replayedRequestBody struct {
	io.Reader
	io.Closer
}

func debugRequestBody(r *http.Request) string {
	if r == nil || r.Body == nil {
		return ""
	}
	contentType := r.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || !isDisplayableRequestBody(mediaType) {
		return omittedBinaryBodyText
	}
	return sanitizeRequestBody(readRequestBody(r), mediaType)
}

func isDisplayableRequestBody(mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return strings.HasPrefix(mediaType, "text/") ||
		isJSONMediaType(mediaType) ||
		mediaType == "application/x-www-form-urlencoded" ||
		mediaType == "application/xml" ||
		strings.HasSuffix(mediaType, "+xml")
}

func sanitizeRequestURL(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	cloned := *r.URL
	cloned.Path = sanitizeExceptionText(cloned.Path)
	cloned.RawPath = ""
	cloned.Opaque = sanitizeExceptionText(cloned.Opaque)
	cloned.Fragment = sanitizeExceptionText(cloned.Fragment)
	cloned.RawFragment = ""
	query := cloned.Query()
	for key, values := range query {
		for index := range values {
			if isSensitiveKey(key) {
				values[index] = redactedPlaceholder
				continue
			}
			values[index] = sanitizeExceptionText(values[index])
		}
		query[key] = values
	}
	cloned.RawQuery = query.Encode()
	if cloned.User != nil {
		cloned.User = url.User(redactedPlaceholder)
	}
	return normalizeExceptionText(cloned.String())
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
		} else {
			for index := range clonedValues {
				clonedValues[index] = sanitizeExceptionText(clonedValues[index])
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

	mediaType, _, _ := mime.ParseMediaType(contentType)
	if isJSONMediaType(mediaType) {
		if sanitizedJSON, ok := sanitizeJSONBody(body); ok {
			return sanitizedJSON
		}
		return "[INVALID JSON BODY OMITTED]"
	}

	if mediaType == "application/x-www-form-urlencoded" {
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
		return "[INVALID FORM BODY OMITTED]"
	}

	return strings.ToValidUTF8(frameworkLog.SanitizeErrorTextFully(body), "�")
}
