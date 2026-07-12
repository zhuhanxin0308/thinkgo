package context

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"regexp"
	"strings"
)

var jsonpCallbackPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(?:\.[A-Za-z_$][A-Za-z0-9_$]*)*$`)

const internalServerErrorMessage = "internal server error"

var (
	// ErrInvalidResponseStatus 表示响应状态码不能作为最终 HTTP 响应状态。
	ErrInvalidResponseStatus = errors.New("响应状态码非法")
	// ErrInvalidResponseHeader 表示响应头名称或值包含非法字符。
	ErrInvalidResponseHeader = errors.New("响应头非法")
	// ErrInvalidRedirect 表示重定向目标、状态码或参数数量非法。
	ErrInvalidRedirect = errors.New("重定向响应非法")
	// ErrInvalidStream 表示流式响应回调为空。
	ErrInvalidStream = errors.New("流式响应回调不能为空")
	// ErrInvalidResponseWriter 表示发送响应时没有可用的底层写入器。
	ErrInvalidResponseWriter = errors.New("响应写入器不能为空")
	// ErrResponseSerialization 表示响应实体无法安全序列化。
	ErrResponseSerialization = errors.New("响应实体序列化失败")
)

// Response 封装框架响应对象，负责统一管理状态码、头部和输出内容。
type Response struct {
	status       int
	header       http.Header
	body         []byte
	filePath     string
	fileRoot     string
	streamWriter func(io.Writer) error
	chunks       [][]byte
	err          error
}

type responseTransmissionError struct {
	err error
}

func (e *responseTransmissionError) Error() (message string) {
	if e == nil || e.err == nil {
		return "响应传输失败"
	}
	message = "响应传输失败"
	defer func() {
		if recover() != nil {
			message = "响应传输失败"
		}
	}()
	return e.err.Error()
}

func (e *responseTransmissionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// IsResponseTransmissionError 判断发送错误是否会让尚未提交的响应处于不完整状态。
func IsResponseTransmissionError(err error) bool {
	var transmissionErr *responseTransmissionError
	return errors.As(err, &transmissionErr)
}

func markResponseTransmissionError(err error) error {
	if err == nil {
		return nil
	}
	return &responseTransmissionError{err: err}
}

// GetBody 获取响应体字节。
func (r *Response) GetBody() []byte {
	if r == nil {
		return nil
	}
	return append([]byte(nil), r.body...)
}

// GetStatus 获取 HTTP 状态码。
func (r *Response) GetStatus() int {
	if r == nil {
		return 0
	}
	return r.status
}

// Error 返回构建或序列化响应时累计的错误。
func (r *Response) Error() error {
	if r == nil {
		return ErrInvalidResponseWriter
	}
	return r.err
}

// NewResponse 创建响应实例。
func NewResponse() *Response {
	return &Response{
		status: http.StatusOK,
		header: make(http.Header),
	}
}

// Header 设置响应头。
func (r *Response) Header(key, value string) *Response {
	return r.setHeader(key, value, false)
}

// AddHeader 追加可重复响应头值，适用于 Set-Cookie 等多值字段。
func (r *Response) AddHeader(key, value string) *Response {
	return r.setHeader(key, value, true)
}

func (r *Response) setHeader(key, value string, appendValue bool) *Response {
	if r == nil {
		return nil
	}
	key = strings.TrimSpace(key)
	if !isValidHeaderName(key) {
		r.addError(fmt.Errorf("%w: 名称 %q", ErrInvalidResponseHeader, key))
		return r
	}
	if isManagedResponseHeader(key) {
		r.addError(fmt.Errorf("%w: %s 由 HTTP 内核管理", ErrInvalidResponseHeader, key))
		return r
	}
	if r.header == nil {
		r.header = make(http.Header)
	}
	sanitized, changed := sanitizeHeaderValue(value)
	if changed {
		r.addError(fmt.Errorf("%w: %s 的值包含控制字符", ErrInvalidResponseHeader, key))
		return r
	}
	if appendValue {
		r.header.Add(key, sanitized)
	} else {
		r.header.Set(key, sanitized)
	}
	return r
}

// Headers 获取全部响应头。
func (r *Response) Headers() http.Header {
	if r == nil {
		return nil
	}
	return r.header.Clone()
}

// Code 设置状态码。
func (r *Response) Code(code int) *Response {
	if r == nil {
		return nil
	}
	if !isValidFinalResponseStatus(code) {
		r.status = http.StatusInternalServerError
		r.addError(fmt.Errorf("%w: %d", ErrInvalidResponseStatus, code))
		return r
	}
	r.status = code
	return r
}

// Content 设置文本内容。
func (r *Response) Content(content string) *Response {
	if r == nil {
		return nil
	}
	r.clearEntitySource()
	r.body = []byte(content)
	return r
}

// Json 设置 JSON 响应。
func (r *Response) Json(data interface{}) *Response {
	if r == nil {
		return nil
	}
	r.clearEntitySource()
	r.Header("Content-Type", "application/json")
	r.Header("X-Content-Type-Options", "nosniff")
	bytes, err := json.Marshal(data)
	if err != nil {
		r.addError(fmt.Errorf("%w: JSON: %v", ErrResponseSerialization, err))
		r.status = http.StatusInternalServerError
		r.body = []byte(`{"message":"` + internalServerErrorMessage + `"}`)
		return r
	}
	r.body = bytes
	return r
}

// Jsonp 设置 JSONP 响应，并严格校验回调名，避免脚本注入。
func (r *Response) Jsonp(callback string, data interface{}) *Response {
	if r == nil {
		return nil
	}
	if !jsonpCallbackPattern.MatchString(callback) {
		return r.Abort(http.StatusBadRequest, map[string]interface{}{
			"message": "invalid jsonp callback",
		})
	}

	payload, err := json.Marshal(data)
	if err != nil {
		r.clearEntitySource()
		r.addError(fmt.Errorf("%w: JSONP: %v", ErrResponseSerialization, err))
		r.Header("Content-Type", "application/javascript; charset=utf-8")
		r.Header("X-Content-Type-Options", "nosniff")
		r.status = http.StatusInternalServerError
		r.body = []byte(fmt.Sprintf("%s({\"message\":\"%s\"});", callback, internalServerErrorMessage))
		return r
	}

	r.clearEntitySource()
	r.Header("Content-Type", "application/javascript; charset=utf-8")
	r.Header("X-Content-Type-Options", "nosniff")
	r.body = []byte(fmt.Sprintf("%s(%s);", callback, payload))
	return r
}

// Redirect 设置重定向响应。
// target 中的 CR/LF 等控制字符会被剥离，防止 Location 头注入与 HTTP 响应拆分。
// 注意：本方法不限制跳转目标的来源，若 target 来自用户输入，调用方需自行防范开放重定向。
func (r *Response) Redirect(target string, code ...int) *Response {
	if r == nil {
		return nil
	}
	status := http.StatusFound
	if len(code) > 1 {
		r.addError(fmt.Errorf("%w: 状态码只能提供一个", ErrInvalidRedirect))
		r.status = http.StatusInternalServerError
		return r
	}
	if len(code) == 1 {
		status = code[0]
	}
	target = strings.TrimSpace(target)
	_, changed := sanitizeHeaderValue(target)
	if target == "" || changed || !isRedirectResponseStatus(status) {
		r.addError(fmt.Errorf("%w: target=%q status=%d", ErrInvalidRedirect, target, status))
		r.status = http.StatusInternalServerError
		if r.header != nil {
			r.header.Del("Location")
		}
		return r
	}
	r.Header("Location", target)
	r.status = status
	return r
}

// sanitizeHeaderValue 移除可能导致响应头注入/拆分的控制字符（CR、LF、NUL）。
func sanitizeHeaderValue(value string) (string, bool) {
	changed := false
	sanitized := strings.Map(func(char rune) rune {
		if char == '\t' {
			return char
		}
		if char < 0x20 || char == 0x7f {
			changed = true
			return -1
		}
		return char
	}, value)
	return sanitized, changed
}

// isValidHeaderName 按 HTTP token 规则校验响应头名，非法头名直接拒绝写入。
func isValidHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		if !isHeaderTokenChar(name[index]) {
			return false
		}
	}
	return true
}

// isHeaderTokenChar 判断字符是否可用于 HTTP 头字段名。
func isHeaderTokenChar(char byte) bool {
	if char >= 'a' && char <= 'z' {
		return true
	}
	if char >= 'A' && char <= 'Z' {
		return true
	}
	if char >= '0' && char <= '9' {
		return true
	}
	switch char {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

func isManagedResponseHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Connection", "Content-Length", "Keep-Alive", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}

// Stream 设置流式响应回调。
func (r *Response) Stream(writer func(io.Writer) error) *Response {
	if r == nil {
		return nil
	}
	r.clearEntitySource()
	if writer == nil {
		r.status = http.StatusInternalServerError
		r.body = []byte(internalServerErrorMessage)
		r.addError(ErrInvalidStream)
		return r
	}
	r.streamWriter = writer
	if r.header.Get("Content-Type") == "" {
		r.Header("Content-Type", "application/octet-stream")
	}
	return r
}

// Chunk 设置分块响应数据。
func (r *Response) Chunk(chunks [][]byte) *Response {
	if r == nil {
		return nil
	}
	r.clearEntitySource()
	r.chunks = make([][]byte, 0, len(chunks))
	for _, chunk := range chunks {
		if len(chunk) == 0 {
			continue
		}
		copied := append([]byte(nil), chunk...)
		r.chunks = append(r.chunks, copied)
	}
	if r.header != nil {
		// 传输分帧由 net/http 按协议版本决定，应用层不得写入 hop-by-hop 头。
		r.header.Del("Transfer-Encoding")
		r.header.Del("Content-Length")
	}
	return r
}

// NoContent 设置 204 空响应。
func (r *Response) NoContent() *Response {
	if r == nil {
		return nil
	}
	r.status = http.StatusNoContent
	r.clearEntitySource()
	if r.header != nil {
		r.header.Del("Content-Length")
		r.header.Del("Transfer-Encoding")
	}
	return r
}

// Abort 立即生成指定状态码的响应，支持字符串和结构化 JSON。
func (r *Response) Abort(code int, payload interface{}) *Response {
	if r == nil {
		return nil
	}
	r.clearEntitySource()
	r.Code(code)
	switch typed := payload.(type) {
	case nil:
		return r
	case string:
		r.Content(typed)
	default:
		r.Json(typed)
		r.Code(code)
	}
	return r
}

// Send 输出响应到客户端，并把构建、文件、流和底层写入错误返回给调用方。
func (r *Response) Send(w http.ResponseWriter) error {
	if isNilHTTPResponseWriter(w) {
		return ErrInvalidResponseWriter
	}
	if r == nil {
		return errors.Join(ErrInvalidResponseWriter, markResponseTransmissionError(writeInternalServerError(w)))
	}
	if errors.Is(r.err, ErrInvalidResponseStatus) || errors.Is(r.err, ErrInvalidRedirect) ||
		errors.Is(r.err, ErrInvalidStream) || errors.Is(r.err, ErrResponseSerialization) {
		return errors.Join(r.err, markResponseTransmissionError(writeInternalServerError(w)))
	}
	if !isValidFinalResponseStatus(r.status) {
		err := fmt.Errorf("%w: %d", ErrInvalidResponseStatus, r.status)
		return errors.Join(r.err, err, markResponseTransmissionError(writeInternalServerError(w)))
	}
	if !responseStatusAllowsBody(r.status) {
		r.writeHeaders(w)
		w.WriteHeader(r.status)
		return r.err
	}
	if r.filePath != "" {
		return r.sendFile(w)
	}

	r.writeHeaders(w)
	if r.streamWriter != nil {
		stream := &deferredStreamWriter{writer: w, status: r.status}
		streamErr := r.streamWriter(stream)
		if streamErr == nil && stream.writeErr == nil {
			stream.commit()
		}
		return errors.Join(r.err, markResponseTransmissionError(errors.Join(streamErr, stream.writeErr)))
	}
	if len(r.chunks) > 0 {
		w.WriteHeader(r.status)
		for _, chunk := range r.chunks {
			if err := writeCompleteResponseBody(w, chunk); err != nil {
				return errors.Join(r.err, markResponseTransmissionError(err))
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		return r.err
	}

	w.WriteHeader(r.status)
	if len(r.body) == 0 {
		return r.err
	}
	err := writeCompleteResponseBody(w, r.body)
	return errors.Join(r.err, markResponseTransmissionError(err))
}

type deferredStreamWriter struct {
	writer    http.ResponseWriter
	status    int
	committed bool
	writeErr  error
}

func (w *deferredStreamWriter) Write(body []byte) (int, error) {
	if len(body) == 0 {
		return 0, nil
	}
	w.commit()
	written, err := w.writer.Write(body)
	if err == nil && written != len(body) {
		err = io.ErrShortWrite
	}
	w.writeErr = errors.Join(w.writeErr, err)
	return written, err
}

func (w *deferredStreamWriter) commit() {
	if w.committed {
		return
	}
	w.writer.WriteHeader(w.status)
	w.committed = true
}

// Cookie 设置 Cookie。
func (r *Response) Cookie(name, value string, maxAge int, path, domain string, secure, httpOnly bool) *Response {
	if r == nil {
		return nil
	}
	cookie := &http.Cookie{
		Name:     name,
		Value:    value,
		MaxAge:   maxAge,
		Path:     path,
		Domain:   domain,
		Secure:   secure,
		HttpOnly: httpOnly,
	}
	if err := cookie.Valid(); err != nil {
		r.addError(fmt.Errorf("%w: Cookie: %v", ErrInvalidResponseHeader, err))
		return r
	}
	r.AddHeader("Set-Cookie", cookie.String())
	return r
}

// Xml 设置 XML 响应。
func (r *Response) Xml(data interface{}) *Response {
	if r == nil {
		return nil
	}
	r.clearEntitySource()
	r.Header("Content-Type", "application/xml")
	r.Header("X-Content-Type-Options", "nosniff")
	bytes, err := xml.Marshal(data)
	if err != nil {
		r.addError(fmt.Errorf("%w: XML: %v", ErrResponseSerialization, err))
		r.status = http.StatusInternalServerError
		r.body = []byte(`<error><message>` + internalServerErrorMessage + `</message></error>`)
		return r
	}
	r.body = bytes
	return r
}

// ContentType 设置 Content-Type。
func (r *Response) ContentType(contentType string, charset ...string) *Response {
	if r == nil {
		return nil
	}
	if len(charset) > 1 {
		r.addError(fmt.Errorf("%w: Content-Type 只能提供一个字符集", ErrInvalidResponseHeader))
		return r
	}
	mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil || mediaType == "" {
		r.addError(fmt.Errorf("%w: Content-Type %q", ErrInvalidResponseHeader, contentType))
		return r
	}
	if len(charset) == 1 {
		value := strings.TrimSpace(charset[0])
		if value == "" {
			r.addError(fmt.Errorf("%w: charset 不能为空", ErrInvalidResponseHeader))
			return r
		}
		parameters["charset"] = value
	}
	formatted := mime.FormatMediaType(mediaType, parameters)
	if formatted == "" {
		r.addError(fmt.Errorf("%w: Content-Type %q", ErrInvalidResponseHeader, contentType))
		return r
	}
	r.Header("Content-Type", formatted)
	return r
}

// Expires 设置过期时间。
func (r *Response) Expires(time string) *Response {
	r.Header("Expires", time)
	return r
}

// LastModified 设置最后修改时间。
func (r *Response) LastModified(time string) *Response {
	r.Header("Last-Modified", time)
	return r
}

// CacheControl 设置缓存控制。
func (r *Response) CacheControl(cache string) *Response {
	r.Header("Cache-Control", cache)
	return r
}

// ETag 设置 ETag。
func (r *Response) ETag(eTag string) *Response {
	r.Header("ETag", eTag)
	return r
}

func (r *Response) addError(err error) {
	if r == nil || err == nil {
		return
	}
	r.err = errors.Join(r.err, err)
}

// clearEntitySource 清除互斥的实体来源，避免链式切换响应类型后继续发送旧文件或旧流。
func (r *Response) clearEntitySource() {
	if r == nil {
		return
	}
	r.body = nil
	r.filePath = ""
	r.fileRoot = ""
	r.streamWriter = nil
	r.chunks = nil
}

func (r *Response) writeHeaders(w http.ResponseWriter) {
	for key, values := range r.header {
		w.Header().Del(key)
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
}

func isValidFinalResponseStatus(code int) bool {
	return code >= http.StatusOK && code <= 599
}

func isRedirectResponseStatus(code int) bool {
	switch code {
	case http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func responseStatusAllowsBody(code int) bool {
	return code != http.StatusNoContent && code != http.StatusResetContent && code != http.StatusNotModified
}

func writeInternalServerError(w http.ResponseWriter) error {
	if isNilHTTPResponseWriter(w) {
		return ErrInvalidResponseWriter
	}
	header := w.Header()
	for key := range header {
		header.Del(key)
	}
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusInternalServerError)
	return writeCompleteResponseBody(w, []byte(http.StatusText(http.StatusInternalServerError)))
}

func isNilHTTPResponseWriter(writer http.ResponseWriter) bool {
	if writer == nil {
		return true
	}
	value := reflect.ValueOf(writer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func writeCompleteResponseBody(w io.Writer, body []byte) error {
	written, err := w.Write(body)
	if err == nil && written != len(body) {
		return io.ErrShortWrite
	}
	return err
}
