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
	"strconv"
	"strings"

	frameworksession "github.com/zhuhanxin0308/thinkgo/v3/session"
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
	identity     *ResponseIdentity
	status       int
	header       *responseHeaders
	body         []byte
	data         interface{}
	options      map[string]interface{}
	allowCache   bool
	filePath     string
	fileRoot     string
	streamWriter func(io.Writer) error
	chunks       [][]byte
	err          error
	committed    bool
	session      *frameworksession.Session
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

// GetCode 获取 HTTP 状态码，对应 ThinkPHP Response.getCode。
func (r *Response) GetCode() int {
	return r.GetStatus()
}

// GetContent 获取最终输出内容。
func (r *Response) GetContent() string {
	if r == nil {
		return ""
	}
	return string(r.body)
}

// GetData 获取响应保留的原始数据。
func (r *Response) GetData() interface{} {
	if r == nil {
		return nil
	}
	return r.data
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
		identity:   &ResponseIdentity{},
		status:     http.StatusOK,
		header:     &responseHeaders{contentType: defaultResponseContentType, inlineContentType: true},
		data:       "",
		allowCache: true,
	}
}

// NewCommittedResponse 表示响应已经由标准库处理器直接写出，框架不得重复发送实体。
func NewCommittedResponse(status int) *Response {
	response := NewResponse()
	if !isValidFinalResponseStatus(status) {
		response.addError(ErrInvalidResponseStatus)
		return response
	}
	response.status = status
	response.committed = true
	return response
}

// Committed 判断响应是否已经通过底层 ResponseWriter 写出。
func (r *Response) Committed() bool {
	return r != nil && r.committed
}

// Header 设置响应头。无参数时保持当前响应；一个参数接受响应头映射；
// 两个字符串参数保留 Go 调用方常用的单项设置方式。
func (r *Response) Header(arguments ...interface{}) *Response {
	if r == nil {
		return nil
	}
	switch len(arguments) {
	case 0:
		return r
	case 1:
		return r.setHeaderCollection(arguments[0])
	case 2:
		key, keyOK := arguments[0].(string)
		value, valueOK := arguments[1].(string)
		if !keyOK || !valueOK {
			r.addError(fmt.Errorf("%w: Header 单项设置必须使用名称和值两个字符串", ErrInvalidResponseHeader))
			return r
		}
		return r.setHeader(key, value, false)
	default:
		r.addError(fmt.Errorf("%w: Header 参数数量为 %d", ErrInvalidResponseHeader, len(arguments)))
		return r
	}
}

func (r *Response) setHeaderCollection(collection interface{}) *Response {
	switch headers := collection.(type) {
	case map[string]string:
		for name, value := range headers {
			r.setHeader(name, value, false)
		}
	case map[string]interface{}:
		for name, rawValue := range headers {
			value, ok := rawValue.(string)
			if !ok {
				r.addError(fmt.Errorf("%w: %s 的值必须是字符串", ErrInvalidResponseHeader, name))
				continue
			}
			r.setHeader(name, value, false)
		}
	case http.Header:
		for name, values := range headers {
			for index, value := range values {
				r.setHeader(name, value, index > 0)
			}
		}
	default:
		r.addError(fmt.Errorf("%w: Header 不支持类型 %T", ErrInvalidResponseHeader, collection))
	}
	return r
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
		r.header = &responseHeaders{}
	}
	sanitized, changed := sanitizeHeaderValue(value)
	if changed {
		r.addError(fmt.Errorf("%w: %s 的值包含控制字符", ErrInvalidResponseHeader, key))
		return r
	}
	r.header.set(http.CanonicalHeaderKey(key), sanitized, appendValue)
	return r
}

// Headers 获取全部响应头。
func (r *Response) Headers() http.Header {
	if r == nil {
		return nil
	}
	return r.header.snapshot()
}

// GetHeader 获取指定响应头；不存在时返回空字符串。
// 全量头部继续由 Headers 返回，避免 Go 调用方处理动态返回类型。
func (r *Response) GetHeader(name string) string {
	if r == nil || r.header == nil {
		return ""
	}
	return r.header.get(name)
}

// Options 合并响应输出选项。
func (r *Response) Options(options map[string]interface{}) *Response {
	if r == nil {
		return nil
	}
	if len(options) == 0 {
		return r
	}
	if r.options == nil {
		r.options = make(map[string]interface{})
	}
	for name, value := range options {
		r.options[name] = value
	}
	return r
}

// Data 设置响应原始数据，并按 HTML 基础响应支持的标量规则生成内容。
func (r *Response) Data(data interface{}) *Response {
	if r == nil {
		return nil
	}
	r.data = data
	r.clearEntitySource()
	switch value := data.(type) {
	case nil:
		r.body = []byte{}
	case string:
		r.body = []byte(value)
	case []byte:
		r.body = append([]byte(nil), value...)
	case bool:
		r.body = []byte(strconv.FormatBool(value))
	case int:
		r.body = []byte(strconv.Itoa(value))
	case int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		r.body = []byte(fmt.Sprint(value))
	default:
		r.addError(fmt.Errorf("%w: HTML 响应数据类型 %T", ErrResponseSerialization, data))
	}
	return r
}

// AllowCache 设置是否允许请求缓存。
func (r *Response) AllowCache(allow bool) *Response {
	if r == nil {
		return nil
	}
	r.allowCache = allow
	return r
}

// IsAllowCache 返回是否允许请求缓存。
func (r *Response) IsAllowCache() bool {
	return r != nil && r.allowCache
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

// Content 设置页面输出内容，接受 ThinkPHP 支持的 nil、字符串、数字和可字符串化对象。
func (r *Response) Content(content interface{}) *Response {
	if r == nil {
		return nil
	}
	text, err := responseContentString(content)
	if err != nil {
		r.addError(err)
		return r
	}
	r.clearEntitySource()
	if r.header == nil {
		r.header = &responseHeaders{}
	}
	if r.header.get("Content-Type") == "" {
		r.header.set("Content-Type", defaultResponseContentType, false)
	}
	r.body = []byte(text)
	return r
}

// Json 设置 JSON 响应。
func (r *Response) Json(data interface{}) *Response {
	if r == nil {
		return nil
	}
	r.clearEntitySource()
	r.data = data
	r.Header("Content-Type", "application/json; charset=utf-8")
	bytes, err := json.Marshal(NormalizeJSONTimes(data))
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

	payload, err := json.Marshal(NormalizeJSONTimes(data))
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
	r.data = data
	r.Header("Content-Type", "application/javascript; charset=utf-8")
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
			r.header.values.Del("Location")
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
	if r.GetHeader("Content-Type") == "" {
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
		r.header.values.Del("Transfer-Encoding")
		r.header.values.Del("Content-Length")
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
		r.header.values.Del("Content-Length")
		r.header.values.Del("Transfer-Encoding")
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
	if r != nil && r.committed {
		return r.err
	}
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
		if err := r.saveSession(w); err != nil {
			return errors.Join(r.err, err)
		}
		w.WriteHeader(r.status)
		return r.err
	}
	if r.filePath != "" {
		return r.sendFile(w)
	}

	r.writeHeaders(w)
	if err := r.saveSession(w); err != nil {
		return errors.Join(r.err, err)
	}
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

// Header 和 WriteHeader 保留完整 ResponseWriter 契约，让流回调可使用 ResponseController。
func (w *deferredStreamWriter) Header() http.Header { return w.writer.Header() }

func (w *deferredStreamWriter) WriteHeader(status int) {
	if !w.committed {
		// 临时响应不占用最终提交机会，协议升级 101 则遵循标准库的最终响应语义。
		if status >= http.StatusContinue && status < http.StatusOK && status != http.StatusSwitchingProtocols {
			w.writer.WriteHeader(status)
			return
		}
		w.status = status
		w.commit()
	}
}

// Unwrap 允许标准库控制器设置逐请求截止时间，而不跳过流自身的写出和刷新边界。
func (w *deferredStreamWriter) Unwrap() http.ResponseWriter { return w.writer }

// FlushError 提交并刷新小片段；刷新失败会保留到最终 Send 返回值中。
func (w *deferredStreamWriter) FlushError() error {
	w.commit()
	err := http.NewResponseController(w.writer).Flush()
	w.writeErr = errors.Join(w.writeErr, err)
	return err
}

// Flush 兼容 http.Flusher；需要同步处理失败的回调应使用 FlushError。
func (w *deferredStreamWriter) Flush() { _ = w.FlushError() }

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

// Xml 设置 XML 响应。
func (r *Response) Xml(data interface{}) *Response {
	if r == nil {
		return nil
	}
	r.clearEntitySource()
	r.data = data
	r.Header("Content-Type", "application/xml; charset=utf-8")
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
	if r.header == nil {
		return
	}
	if r.header.inlineContentType {
		w.Header().Set("Content-Type", r.header.contentType)
	}
	for key, values := range r.header.values {
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
