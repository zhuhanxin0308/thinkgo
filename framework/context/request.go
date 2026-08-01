package context

import (
	"bytes"
	stdcontext "context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

const (
	// DefaultMultipartMemoryLimit 是 multipart 表单保留在内存中的默认上限，超出部分写入临时文件。
	DefaultMultipartMemoryLimit int64 = 32 << 20
	// DefaultMaxBodyBytes 是独立使用 Request 时的默认请求体硬上限。
	DefaultMaxBodyBytes int64 = 10 << 20
)

var (
	// ErrRequestBodyTooLarge 表示请求体超过配置的硬上限。
	ErrRequestBodyTooLarge = errors.New("请求体超过大小上限")
	// ErrRequestBodyUnavailable 表示 multipart 流已被解析，原始请求体不能再可靠读取。
	ErrRequestBodyUnavailable = errors.New("请求体已被 multipart 解析消费")
	// ErrInvalidJSONBody 表示 JSON 文档不完整、不唯一或结构非法。
	ErrInvalidJSONBody = errors.New("JSON 请求体非法")
	// ErrInvalidFormBody 表示表单请求体解析失败。
	ErrInvalidFormBody = errors.New("表单请求体非法")
	// ErrInvalidContentType 表示 Content-Type 请求头语法非法。
	ErrInvalidContentType = errors.New("Content-Type 非法")
	// ErrRequestCleaned 表示请求资源已清理，不能再次解析上传文件。
	ErrRequestCleaned = errors.New("请求资源已清理")
)

// Request 封装原生 HTTP 请求，并为参数解析、上传清理和代理解析提供并发安全边界。
type Request struct {
	raw *http.Request

	applicationMu sync.RWMutex
	application   *ApplicationContext

	dataMu      sync.RWMutex
	data        map[string]interface{}
	routeParams map[string]interface{}

	routeMu sync.RWMutex

	bodyOnce  sync.Once
	bodyCache []byte

	queryOnce  sync.Once
	queryCache url.Values

	contentTypeOnce sync.Once
	mediaType       string
	contentTypeErr  error

	jsonOnce      sync.Once
	jsonBody      map[string]interface{}
	jsonStateMu   sync.RWMutex
	jsonValidated bool

	formOnce   sync.Once
	formMu     sync.Mutex
	cleaned    bool
	cleanupErr error

	errorMu sync.RWMutex
	bodyErr error
	jsonErr error
	formErr error

	trustedProxies       *TrustedProxySet
	multipartMemoryLimit int64
	maxBodyBytes         int64
}

// NewRequest 创建请求包装器，并严格校验所有安全相关选项。
func NewRequest(raw *http.Request, options ...RequestOption) (*Request, error) {
	req := &Request{
		raw:                  raw,
		multipartMemoryLimit: DefaultMultipartMemoryLimit,
		maxBodyBytes:         DefaultMaxBodyBytes,
	}
	for index, option := range options {
		if option == nil {
			return nil, fmt.Errorf("%w: 第 %d 项", ErrInvalidRequestOption, index+1)
		}
		if err := option(req); err != nil {
			return nil, fmt.Errorf("应用第 %d 个请求选项失败: %w", index+1, err)
		}
	}

	if raw != nil && raw.Body != nil && raw.Body != http.NoBody {
		raw.Body = newBodyLimitReadCloser(raw.Body, req.maxBodyBytes)
	}
	return req, nil
}

// MustNewRequest 为固定且可信的内部配置提供便捷构造，配置错误会立即触发 panic。
func MustNewRequest(raw *http.Request, options ...RequestOption) *Request {
	req, err := NewRequest(raw, options...)
	if err != nil {
		panic(err)
	}
	return req
}

// Method 获取请求方法。
func (r *Request) Method() string {
	if r == nil || r.raw == nil {
		return ""
	}
	return r.raw.Method
}

// Host 获取请求主机。
func (r *Request) Host() string {
	if r == nil || r.raw == nil {
		return ""
	}
	return r.raw.Host
}

// Path 获取请求路径。
func (r *Request) Path() string {
	if r == nil || r.raw == nil || r.raw.URL == nil {
		return ""
	}
	return r.raw.URL.Path
}

// Pathinfo 是 Path 的兼容别名。
func (r *Request) Pathinfo() string {
	return r.Path()
}

// Ext 获取当前 URL 最后一个路径段的后缀，不含点号。
func (r *Request) Ext() string {
	path := r.Path()
	for index := len(path) - 1; index >= 0; index-- {
		if path[index] == '.' {
			return path[index+1:]
		}
		if path[index] == '/' {
			break
		}
	}
	return ""
}

// Param 按“路由参数 > 请求体参数 > 查询参数”的优先级读取标量参数。
func (r *Request) Param(key string, defaults ...string) string {
	if value, ok := r.paramValue(key); ok {
		if text, valid := stringifyRequestValue(value); valid {
			return text
		}
	}
	return firstDefault(defaults)
}

// Get 获取查询参数，显式空值不会被默认值覆盖。
func (r *Request) Get(key string, defaults ...string) string {
	if values, ok := r.queryValues()[key]; ok && len(values) > 0 {
		return values[0]
	}
	return firstDefault(defaults)
}

// Post 获取表单或 JSON 请求体中的标量参数，显式空值不会被默认值覆盖。
func (r *Request) Post(key string, defaults ...string) string {
	mediaType, _ := r.parsedMediaType()
	if isFormMediaType(mediaType) {
		_ = r.ensureFormParsed()
		if r.raw != nil {
			if values, ok := r.raw.PostForm[key]; ok && len(values) > 0 {
				return values[0]
			}
		}
	}

	if isJSONMediaType(mediaType) {
		if value, ok := r.getJSONBodyValue(key); ok {
			if text, valid := stringifyRequestValue(value); valid {
				return text
			}
		}
	}
	return firstDefault(defaults)
}

// Route 仅从路由和中间件透传数据中读取标量参数。
func (r *Request) Route(key string, defaults ...string) string {
	if r == nil {
		return firstDefault(defaults)
	}
	r.routeMu.RLock()
	value, ok := r.routeParams[key]
	r.routeMu.RUnlock()
	if !ok {
		r.dataMu.RLock()
		value, ok = r.data[key]
		r.dataMu.RUnlock()
	}
	if ok {
		if text, valid := stringifyRequestValue(value); valid {
			return text
		}
	}
	return firstDefault(defaults)
}

// All 返回参数快照，嵌套 JSON、切片和字节数据均使用防御性副本。
func (r *Request) All() map[string]interface{} {
	result := make(map[string]interface{})
	for key, values := range r.queryValues() {
		result[key] = normalizeStringSliceValue(values)
	}

	mediaType, _ := r.parsedMediaType()
	if isFormMediaType(mediaType) {
		_ = r.ensureFormParsed()
		if r.raw != nil {
			for key, values := range r.raw.PostForm {
				result[key] = normalizeStringSliceValue(values)
			}
		}
	}
	if isJSONMediaType(mediaType) {
		r.parseJSONBody()
		for key, value := range r.jsonBody {
			result[key] = deepCloneRequestValue(value)
		}
	}

	if r != nil {
		r.dataMu.RLock()
		for key, value := range r.data {
			result[key] = deepCloneRequestValue(value)
		}
		r.dataMu.RUnlock()
		r.routeMu.RLock()
		for key, value := range r.routeParams {
			result[key] = deepCloneRequestValue(value)
		}
		r.routeMu.RUnlock()
	}
	return result
}

// Only 返回指定字段的参数快照，缺失字段使用空字符串保持原 API 语义。
func (r *Request) Only(keys ...string) map[string]interface{} {
	result := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		if value, ok := r.paramValue(key); ok {
			result[key] = deepCloneRequestValue(value)
			continue
		}
		result[key] = ""
	}
	return result
}

// Except 返回排除指定字段后的参数快照。
func (r *Request) Except(keys ...string) map[string]interface{} {
	result := r.All()
	for _, key := range keys {
		delete(result, key)
	}
	return result
}

// PostArray 从 JSON 请求体中提取标量字符串数组，出现复杂对象时拒绝隐式格式化。
func (r *Request) PostArray(key string) []string {
	mediaType, _ := r.parsedMediaType()
	if !isJSONMediaType(mediaType) {
		return nil
	}
	value, ok := r.getJSONBodyValue(key)
	if !ok {
		return nil
	}
	items, ok := value.([]interface{})
	if !ok {
		if stringsValue, valid := value.([]string); valid {
			return append([]string(nil), stringsValue...)
		}
		return nil
	}

	result := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil {
			return nil
		}
		text, valid := stringifyRequestValue(item)
		if !valid {
			return nil
		}
		result = append(result, text)
	}
	return result
}

// Body 返回原始请求体的防御性副本。
func (r *Request) Body() ([]byte, error) {
	body, err := r.readBody()
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), body...), nil
}

// BodyReadError 返回已经发生的请求体读取错误，不主动消费请求体。
func (r *Request) BodyReadError() error {
	if r == nil {
		return nil
	}
	r.errorMu.RLock()
	defer r.errorMu.RUnlock()
	return r.bodyErr
}

// Parse 根据 Content-Type 预解析结构化请求体，用于在业务逻辑前阻断非法输入。
func (r *Request) Parse() error {
	if r == nil {
		return nil
	}
	if r.raw != nil && r.raw.ContentLength > r.maxBodyBytes {
		err := fmt.Errorf("%w: 上限 %d 字节", ErrRequestBodyTooLarge, r.maxBodyBytes)
		r.setBodyError(err)
		return err
	}

	mediaType, err := r.parsedMediaType()
	if err != nil {
		return err
	}
	// 没有请求体时不触发 io.ReadAll 和 ParseForm；查询参数仍由 Get/All 惰性读取。
	// JSON 空体仍需进入严格解析流程，保持空 JSON 请求被拒绝的既有语义。
	if !requestHasBody(r.raw) {
		if isJSONMediaType(mediaType) {
			r.parseJSONBody()
			return r.JSONError()
		}
		return nil
	}
	switch {
	case isJSONMediaType(mediaType):
		r.parseJSONBody()
		return r.JSONError()
	case isFormMediaType(mediaType):
		return r.ensureFormParsed()
	default:
		_, err = r.readBody()
		return err
	}
}

// ParseError 返回目前已发现的内容类型、请求体、JSON 或表单解析错误。
func (r *Request) ParseError() error {
	if r == nil {
		return nil
	}
	_, contentTypeErr := r.parsedMediaType()
	r.errorMu.RLock()
	bodyErr := r.bodyErr
	jsonErr := r.jsonErr
	formErr := r.formErr
	r.errorMu.RUnlock()
	if errors.Is(bodyErr, ErrRequestBodyUnavailable) {
		bodyErr = nil
	}
	return errors.Join(contentTypeErr, bodyErr, jsonErr, formErr)
}

// JSONError 返回 JSON 惰性解析错误。
func (r *Request) JSONError() error {
	if r == nil {
		return nil
	}
	r.errorMu.RLock()
	defer r.errorMu.RUnlock()
	return r.jsonErr
}

// Json 将单一且无重复键的 JSON 文档绑定到目标值。
func (r *Request) Json(target interface{}) error {
	body, err := r.readBody()
	if err != nil {
		return err
	}
	var decodeErr error
	if r.isJSONValidated() {
		decodeErr = decodeJSONTarget(body, target)
	} else {
		decodeErr = decodeStrictJSONTarget(body, target)
	}
	if decodeErr != nil {
		return fmt.Errorf("%w: %v", ErrInvalidJSONBody, decodeErr)
	}
	return nil
}

// Header 获取请求头，显式空值不会被默认值覆盖。
func (r *Request) Header(key string, defaults ...string) string {
	if r == nil || r.raw == nil {
		return firstDefault(defaults)
	}
	canonicalKey := http.CanonicalHeaderKey(key)
	if values, ok := r.raw.Header[canonicalKey]; ok {
		return strings.Join(values, ", ")
	}
	return firstDefault(defaults)
}

// Raw 返回原生请求对象；调用方不得并发修改该对象。
func (r *Request) Raw() *http.Request {
	if r == nil {
		return nil
	}
	return r.raw
}

// Context 返回原生请求上下文；空请求也返回可安全使用的后台上下文。
func (r *Request) Context() stdcontext.Context {
	if r == nil || r.raw == nil || r.raw.Context() == nil {
		return stdcontext.Background()
	}
	return r.raw.Context()
}

// SetApplicationContext 写入当前请求的应用上下文。
// 上下文只在请求分发前设置一次，读取方始终拿到值拷贝。
func (r *Request) SetApplicationContext(application ApplicationContext) {
	if r == nil {
		return
	}
	applicationCopy := application
	r.applicationMu.Lock()
	r.application = &applicationCopy
	r.applicationMu.Unlock()
}

// ApplicationContext 返回当前请求的应用上下文快照。
func (r *Request) ApplicationContext() (ApplicationContext, bool) {
	if r == nil {
		return ApplicationContext{}, false
	}
	r.applicationMu.RLock()
	if r.application == nil {
		r.applicationMu.RUnlock()
		return ApplicationContext{}, false
	}
	application := *r.application
	r.applicationMu.RUnlock()
	return application, true
}

// ApplicationPath 为当前请求生成带应用前缀的站内路径。
func (r *Request) ApplicationPath(path string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("%w: 请求为空", ErrInvalidApplicationPath)
	}
	application, exists := r.ApplicationContext()
	if !exists {
		return BuildApplicationPath("", path)
	}
	return BuildApplicationPath(application.PathPrefix(), path)
}

func (r *Request) IsGet() bool    { return r.Method() == http.MethodGet }
func (r *Request) IsPost() bool   { return r.Method() == http.MethodPost }
func (r *Request) IsPut() bool    { return r.Method() == http.MethodPut }
func (r *Request) IsDelete() bool { return r.Method() == http.MethodDelete }

// IsAjax 判断请求是否由 XMLHttpRequest 发起。
func (r *Request) IsAjax() bool {
	return r.Header("X-Requested-With") == "XMLHttpRequest"
}

// Ip 获取经受信代理链解析后的客户端 IP。
func (r *Request) Ip() string {
	if r == nil || r.raw == nil {
		return ""
	}
	return resolveClientIP(r.raw, r.trustedProxies)
}

// Input 是 Param 的兼容别名。
func (r *Request) Input(key string, defaults ...string) string {
	return r.Param(key, defaults...)
}

// File 获取上传文件元数据；返回值与请求清理生命周期绑定。
func (r *Request) File(key string) (*multipart.FileHeader, error) {
	if r == nil || r.raw == nil {
		return nil, errors.New("原生请求为空")
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("上传字段名不能为空")
	}
	mediaType, err := r.parsedMediaType()
	if err != nil {
		return nil, err
	}
	if mediaType != "multipart/form-data" {
		return nil, fmt.Errorf("%w: 当前类型为 %q", ErrInvalidFormBody, mediaType)
	}
	if err := r.ensureFormParsed(); err != nil {
		return nil, err
	}

	r.formMu.Lock()
	defer r.formMu.Unlock()
	if r.cleaned {
		return nil, ErrRequestCleaned
	}
	if r.raw.MultipartForm == nil {
		return nil, http.ErrMissingFile
	}
	headers := r.raw.MultipartForm.File[key]
	if len(headers) == 0 || headers[0] == nil {
		return nil, http.ErrMissingFile
	}
	copyHeader := *headers[0]
	copyHeader.Header = make(textproto.MIMEHeader, len(headers[0].Header))
	for headerName, values := range headers[0].Header {
		copyHeader.Header[headerName] = append([]string(nil), values...)
	}
	return &copyHeader, nil
}

// Cleanup 幂等释放 multipart 临时文件，并阻止清理后的再次解析。
func (r *Request) Cleanup() error {
	if r == nil {
		return nil
	}
	r.formMu.Lock()
	defer r.formMu.Unlock()
	if r.cleaned {
		return r.cleanupErr
	}
	r.cleaned = true
	if r.raw != nil && r.raw.MultipartForm != nil {
		r.cleanupErr = r.raw.MultipartForm.RemoveAll()
		r.raw.MultipartForm = nil
	}
	return r.cleanupErr
}

// Cookie 获取 Cookie 值。
func (r *Request) Cookie(key string) string {
	if r == nil || r.raw == nil {
		return ""
	}
	cookie, err := r.raw.Cookie(key)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// Has 判断参数是否存在，显式 null 或空字符串也视为存在。
func (r *Request) Has(key string) bool {
	_, ok := r.paramValue(key)
	return ok
}

// IsSsl 判断原生 TLS 或受信代理链是否声明 HTTPS。
func (r *Request) IsSsl() bool {
	return r != nil && r.raw != nil && isHTTPS(r.raw, r.trustedProxies)
}

// Scheme 获取请求协议。
func (r *Request) Scheme() string {
	if r.IsSsl() {
		return "https"
	}
	return "http"
}

// Port 获取显式端口，未提供时按协议返回默认端口。
func (r *Request) Port() string {
	if r != nil && r.raw != nil {
		host := strings.TrimSpace(r.raw.Host)
		if host != "" && !isValidAbsoluteRequestHost(host) {
			return ""
		}
		if _, port, err := net.SplitHostPort(host); err == nil {
			return port
		}
	}
	if r.IsSsl() {
		return "443"
	}
	return "80"
}

// Url 返回请求目标；complete=true 时返回带协议和主机的绝对地址。
func (r *Request) Url(complete ...bool) string {
	if r == nil || r.raw == nil || r.raw.URL == nil {
		return ""
	}
	if len(complete) == 0 || !complete[0] {
		return r.raw.URL.RequestURI()
	}
	host := r.absoluteHost()
	if host == "" {
		return ""
	}
	copyURL := *r.raw.URL
	copyURL.Scheme = r.Scheme()
	copyURL.Host = host
	return copyURL.String()
}

// BaseUrl 获取不带查询字符串的绝对地址。
func (r *Request) BaseUrl() string {
	if r == nil || r.raw == nil || r.raw.URL == nil {
		return ""
	}
	host := r.absoluteHost()
	if host == "" {
		return ""
	}
	copyURL := *r.raw.URL
	copyURL.Scheme = r.Scheme()
	copyURL.Host = host
	copyURL.RawQuery = ""
	copyURL.ForceQuery = false
	copyURL.Fragment = ""
	return copyURL.String()
}

// Root 获取站点根地址。
func (r *Request) Root() string {
	if r == nil {
		return ""
	}
	host := r.absoluteHost()
	if host == "" {
		return ""
	}
	return r.Scheme() + "://" + host
}

// ContentType 获取原始内容类型，缺失时保持框架既有的表单默认值。
func (r *Request) ContentType() string {
	if r == nil || r.raw == nil {
		return "application/x-www-form-urlencoded"
	}
	if values, ok := r.raw.Header["Content-Type"]; ok {
		return strings.Join(values, ", ")
	}
	return "application/x-www-form-urlencoded"
}

// MediaType 返回去除参数并规范为小写的媒体类型。
func (r *Request) MediaType() (string, error) {
	return r.parsedMediaType()
}

// Server 为兼容旧接口，从请求头读取对应键值。
func (r *Request) Server(key string) string {
	return r.Header(key)
}

// Set 写入中间件或路由透传数据，零值 Request 也可安全使用。
func (r *Request) Set(key string, value interface{}) {
	if r == nil {
		return
	}
	r.dataMu.Lock()
	if r.data == nil {
		r.data = make(map[string]interface{})
	}
	r.data[key] = value
	r.dataMu.Unlock()
}

// SetRoute 写入路由参数独立命名空间，避免用户参数覆盖 Session、调试和终止器等内部数据。
func (r *Request) SetRoute(key string, value interface{}) {
	if r == nil {
		return
	}
	r.routeMu.Lock()
	if r.routeParams == nil {
		r.routeParams = make(map[string]interface{})
	}
	r.routeParams[key] = value
	r.routeMu.Unlock()
}

// GetData 获取透传数据。
func (r *Request) GetData(key string) interface{} {
	if r == nil {
		return nil
	}
	r.dataMu.RLock()
	defer r.dataMu.RUnlock()
	return r.data[key]
}

func (r *Request) queryValues() url.Values {
	if r == nil {
		return url.Values{}
	}
	r.queryOnce.Do(func() {
		if r.raw == nil || r.raw.URL == nil {
			r.queryCache = url.Values{}
			return
		}
		r.queryCache = r.raw.URL.Query()
	})
	return r.queryCache
}

func (r *Request) parsedMediaType() (string, error) {
	if r == nil {
		return "application/x-www-form-urlencoded", nil
	}
	r.contentTypeOnce.Do(func() {
		contentType := r.ContentType()
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			r.contentTypeErr = fmt.Errorf("%w: %v", ErrInvalidContentType, err)
			return
		}
		r.mediaType = strings.ToLower(mediaType)
	})
	return r.mediaType, r.contentTypeErr
}

func (r *Request) readBody() ([]byte, error) {
	if r == nil {
		return []byte{}, nil
	}
	r.bodyOnce.Do(func() {
		if r.raw == nil || r.raw.Body == nil || r.raw.Body == http.NoBody {
			r.bodyCache = []byte{}
			return
		}
		if r.raw.ContentLength > r.maxBodyBytes {
			r.setBodyError(fmt.Errorf("%w: 上限 %d 字节", ErrRequestBodyTooLarge, r.maxBodyBytes))
			_ = r.raw.Body.Close()
			r.raw.Body = http.NoBody
			return
		}

		source := r.raw.Body
		body, readErr := io.ReadAll(source)
		closeErr := source.Close()
		if readErr != nil {
			r.setBodyError(normalizeBodyReadError(readErr, r.maxBodyBytes))
			r.raw.Body = http.NoBody
			return
		}
		if closeErr != nil {
			r.setBodyError(fmt.Errorf("关闭请求体失败: %w", closeErr))
			r.raw.Body = http.NoBody
			return
		}
		r.bodyCache = body
		r.raw.Body = io.NopCloser(bytes.NewReader(body))
	})
	return r.bodyCache, r.BodyReadError()
}

func (r *Request) parseJSONBody() {
	if r == nil {
		return
	}
	r.jsonOnce.Do(func() {
		r.jsonBody = make(map[string]interface{})
		body, err := r.readBody()
		if err != nil {
			r.setJSONError(err)
			return
		}
		payload, err := decodeStrictJSONObject(body)
		if err != nil {
			r.setJSONError(fmt.Errorf("%w: %v", ErrInvalidJSONBody, err))
			return
		}
		r.jsonBody = payload
		r.jsonStateMu.Lock()
		r.jsonValidated = true
		r.jsonStateMu.Unlock()
	})
}

func (r *Request) isJSONValidated() bool {
	if r == nil {
		return false
	}
	r.jsonStateMu.RLock()
	validated := r.jsonValidated
	r.jsonStateMu.RUnlock()
	return validated
}

func (r *Request) ensureFormParsed() error {
	if r == nil || r.raw == nil {
		return nil
	}
	r.formOnce.Do(func() {
		mediaType, err := r.parsedMediaType()
		if err != nil {
			r.setFormError(err)
			return
		}
		switch mediaType {
		case "application/x-www-form-urlencoded":
			body, bodyErr := r.readBody()
			if bodyErr != nil {
				r.setFormError(bodyErr)
				return
			}
			if err := r.raw.ParseForm(); err != nil {
				r.setFormError(fmt.Errorf("%w: %v", ErrInvalidFormBody, err))
				return
			}
			r.raw.Body = io.NopCloser(bytes.NewReader(body))
		case "multipart/form-data":
			r.formMu.Lock()
			defer r.formMu.Unlock()
			if r.cleaned {
				r.setFormError(ErrRequestCleaned)
				return
			}
			// 未缓存的 multipart 必须保持流式解析，随后 Body 会明确报告不可用。
			r.bodyOnce.Do(func() {
				r.setBodyError(ErrRequestBodyUnavailable)
			})
			if err := r.raw.ParseMultipartForm(r.multipartMemoryLimit); err != nil {
				r.setFormError(fmt.Errorf("%w: %w", ErrInvalidFormBody, normalizeBodyReadError(err, r.maxBodyBytes)))
			}
		}
	})
	r.errorMu.RLock()
	defer r.errorMu.RUnlock()
	return r.formErr
}

func (r *Request) getJSONBodyValue(key string) (interface{}, bool) {
	r.parseJSONBody()
	if r == nil || r.JSONError() != nil {
		return nil, false
	}
	value, ok := r.jsonBody[key]
	return value, ok
}

func (r *Request) paramValue(key string) (interface{}, bool) {
	if r == nil {
		return nil, false
	}
	r.routeMu.RLock()
	value, ok := r.routeParams[key]
	r.routeMu.RUnlock()
	if ok {
		return value, true
	}
	r.dataMu.RLock()
	value, ok = r.data[key]
	r.dataMu.RUnlock()
	if ok {
		return value, true
	}

	mediaType, _ := r.parsedMediaType()
	if isFormMediaType(mediaType) {
		_ = r.ensureFormParsed()
		if r.raw != nil {
			if values, exists := r.raw.PostForm[key]; exists && len(values) > 0 {
				return normalizeStringSliceValue(values), true
			}
		}
	}
	if isJSONMediaType(mediaType) {
		if value, exists := r.getJSONBodyValue(key); exists {
			return value, true
		}
	}
	if values, exists := r.queryValues()[key]; exists && len(values) > 0 {
		return normalizeStringSliceValue(values), true
	}
	return nil, false
}

func (r *Request) setBodyError(err error) {
	r.errorMu.Lock()
	r.bodyErr = err
	r.errorMu.Unlock()
}

func (r *Request) setJSONError(err error) {
	r.errorMu.Lock()
	r.jsonErr = err
	r.errorMu.Unlock()
}

func (r *Request) setFormError(err error) {
	r.errorMu.Lock()
	r.formErr = err
	r.errorMu.Unlock()
}

func isJSONMediaType(mediaType string) bool {
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func isFormMediaType(mediaType string) bool {
	return mediaType == "application/x-www-form-urlencoded" || mediaType == "multipart/form-data"
}

// requestHasBody 判断请求是否存在需要框架读取的实体；ContentLength=0 的请求遵循 net/http 语义视为空体。
func requestHasBody(raw *http.Request) bool {
	if raw == nil || raw.Body == nil || raw.Body == http.NoBody {
		return false
	}
	return raw.ContentLength != 0
}

func normalizeBodyReadError(err error, limit int64) error {
	var maxBytesErr *http.MaxBytesError
	if errors.Is(err, ErrRequestBodyTooLarge) || errors.As(err, &maxBytesErr) {
		return fmt.Errorf("%w: 上限 %d 字节", ErrRequestBodyTooLarge, limit)
	}
	return err
}

type bodyLimitReadCloser struct {
	source    io.ReadCloser
	remaining int64
}

func newBodyLimitReadCloser(source io.ReadCloser, limit int64) io.ReadCloser {
	return &bodyLimitReadCloser{source: source, remaining: limit}
}

func (r *bodyLimitReadCloser) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.remaining <= 0 {
		var probe [1]byte
		read, err := r.source.Read(probe[:])
		if read > 0 {
			return 0, ErrRequestBodyTooLarge
		}
		return 0, err
	}
	if r.remaining < int64(^uint64(0)>>1) && int64(len(buffer)) > r.remaining+1 {
		buffer = buffer[:r.remaining+1]
	}
	read, err := r.source.Read(buffer)
	if int64(read) > r.remaining {
		allowed := int(r.remaining)
		r.remaining = 0
		return allowed, ErrRequestBodyTooLarge
	}
	r.remaining -= int64(read)
	return read, err
}

func (r *Request) absoluteHost() string {
	if r == nil || r.raw == nil {
		return ""
	}
	host := strings.TrimSpace(r.raw.Host)
	if host == "" || strings.ContainsAny(host, "\\/@?#\x00\r\n\t ") || !isValidAbsoluteRequestHost(host) {
		return ""
	}
	parsed, err := url.Parse("http://" + host)
	if err != nil || parsed.User != nil || parsed.Host != host || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	hostname := parsed.Hostname()
	if hostname == "" || (net.ParseIP(hostname) == nil && !isValidRequestHostname(hostname)) {
		return ""
	}
	return host
}

// isValidAbsoluteRequestHost 严格校验绝对 URL 使用的主机和可选端口，拒绝服务名端口及畸形 IPv6 括号。
func isValidAbsoluteRequestHost(host string) bool {
	if strings.HasPrefix(host, "[") {
		closingBracket := strings.IndexByte(host, ']')
		if closingBracket <= 1 {
			return false
		}
		ip := net.ParseIP(host[1:closingBracket])
		if ip == nil || ip.To4() != nil {
			return false
		}
		remainder := host[closingBracket+1:]
		return remainder == "" || strings.HasPrefix(remainder, ":") && isValidRequestPort(remainder[1:])
	}
	if strings.ContainsAny(host, "[]") || strings.Count(host, ":") > 1 {
		return false
	}
	hostname := host
	if parsedHost, port, hasPort := strings.Cut(host, ":"); hasPort {
		if !isValidRequestPort(port) {
			return false
		}
		hostname = parsedHost
	}
	hostname = strings.TrimSuffix(hostname, ".")
	return hostname != "" && (net.ParseIP(hostname) != nil || isValidRequestHostname(hostname))
}

func isValidRequestPort(port string) bool {
	if port == "" {
		return false
	}
	for _, char := range port {
		if char < '0' || char > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	return err == nil && parsed > 0
}

func isValidRequestHostname(hostname string) bool {
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	if hostname == "" || len(hostname) > 253 {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func (r *bodyLimitReadCloser) Close() error {
	return r.source.Close()
}
