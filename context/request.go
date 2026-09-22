package context

import (
	"bytes"
	stdcontext "context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	frameworkenv "github.com/zhuhanxin0308/thinkgo/framework/env"
)

const (
	// DefaultMultipartMemoryLimit 是 multipart 表单保留在内存中的默认上限，超出部分写入临时文件。
	DefaultMultipartMemoryLimit int64 = 32 << 20
	// DefaultMaxBodyBytes 是独立使用 Request 时的默认请求体硬上限。
	DefaultMaxBodyBytes int64 = 10 << 20
	// defaultHTTPPort 和 defaultHTTPSPort 对应未显式声明端口时的协议默认值。
	defaultHTTPPort  = 80
	defaultHTTPSPort = 443
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
	// ErrInvalidRequestCleanupContext 表示请求清理收到 nil 上下文。
	ErrInvalidRequestCleanupContext = errors.New("请求清理上下文不能为空")
)

// Request 封装原生 HTTP 请求，并为参数解析、上传清理和代理解析提供并发安全边界。
type Request struct {
	raw               *http.Request
	responseWriter    http.ResponseWriter
	serviceMu         sync.RWMutex
	serviceResolver   ServiceResolver
	serviceClosers    []io.Closer
	serviceIdleCloser IdleServiceScopeCloser
	serviceClosed     bool
	compatibility     atomic.Pointer[requestThinkPHPState]
	compatibilityOnce sync.Once
	createdAt         time.Time
	environment       atomic.Pointer[frameworkenv.Env]
	applicationMu     sync.RWMutex
	application       *ApplicationContext

	metadataMu      sync.RWMutex
	methodValue     string
	urlValue        string
	urlSet          bool
	baseURLValue    string
	baseURLSet      bool
	rootValue       string
	rootSet         bool
	pathinfoValue   string
	pathinfoSet     bool
	hostValue       string
	hostSet         bool
	domainValue     string
	domainSet       bool
	layerValue      string
	controllerValue string
	actionValue     string

	dataMu      sync.RWMutex
	data        map[string]interface{}
	routeParams map[string]interface{}

	routeMu sync.RWMutex

	bodyOnce  sync.Once
	bodyCache []byte

	queryMu    sync.Mutex
	queryText  string
	querySet   bool
	queryCache url.Values
	queryErr   error

	contentTypeMu   sync.Mutex
	contentTypeText string
	contentTypeSet  bool
	mediaType       string
	contentTypeErr  error

	jsonOnce      sync.Once
	jsonTreeOnce  sync.Once
	jsonBody      map[string]interface{}
	jsonStateMu   sync.RWMutex
	jsonValidated bool

	formOnce               sync.Once
	formMu                 sync.Mutex
	cleanupOnce            sync.Once
	cleanupDone            chan struct{}
	cleanupStarted         atomic.Bool
	cleanupNeedsSupervisor atomic.Bool
	cleaned                bool
	cleanupErr             error

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
		createdAt:            time.Now(),
		multipartMemoryLimit: DefaultMultipartMemoryLimit,
		maxBodyBytes:         DefaultMaxBodyBytes,
	}
	// 时间在选项执行前固定，兼容字段仅在实际使用时创建。
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

// Method 获取当前请求方法；origin=true 返回覆盖前的原始方法。
func (r *Request) Method(origin ...bool) string {
	original := http.MethodGet
	if r != nil && r.raw != nil && strings.TrimSpace(r.raw.Method) != "" {
		original = strings.ToUpper(r.raw.Method)
	}
	if len(origin) > 0 && origin[0] {
		return original
	}
	if r == nil {
		return original
	}
	r.metadataMu.RLock()
	method := r.methodValue
	r.metadataMu.RUnlock()
	if method != "" {
		return method
	}
	if r.raw != nil {
		if override := strings.TrimSpace(r.raw.Header.Get("X-HTTP-Method-Override")); override != "" {
			return strings.ToUpper(override)
		}
	}
	return original
}

// SetMethod 设置当前请求方法，不改变 Method(true) 返回的原始方法。
func (r *Request) SetMethod(method string) *Request {
	if r == nil {
		return r
	}
	r.metadataMu.Lock()
	r.methodValue = strings.ToUpper(strings.TrimSpace(method))
	r.metadataMu.Unlock()
	return r
}

// Host 获取请求主机；strict=true 时去除端口。
func (r *Request) Host(strict ...bool) string {
	host := r.requestHostValue()
	if len(strict) > 0 && strict[0] {
		return requestHostname(host)
	}
	return host
}

// SetHost 设置当前请求主机。
func (r *Request) SetHost(host string) *Request {
	if r == nil {
		return r
	}
	r.metadataMu.Lock()
	r.hostValue = host
	r.hostSet = true
	r.metadataMu.Unlock()
	return r
}

// Path 获取请求路径。
func (r *Request) Path() string {
	if r == nil || r.raw == nil || r.raw.URL == nil {
		return ""
	}
	return r.raw.URL.Path
}

// SetPathinfo 设置当前请求的 pathinfo。
func (r *Request) SetPathinfo(pathinfo string) *Request {
	if r == nil {
		return r
	}
	r.metadataMu.Lock()
	r.pathinfoValue = strings.TrimLeft(pathinfo, "/")
	r.pathinfoSet = true
	r.metadataMu.Unlock()
	return r
}

// Pathinfo 获取不含开头斜杠的当前 pathinfo。
func (r *Request) Pathinfo() string {
	if r == nil {
		return ""
	}
	r.metadataMu.RLock()
	if r.pathinfoSet {
		value := r.pathinfoValue
		r.metadataMu.RUnlock()
		return value
	}
	r.metadataMu.RUnlock()
	path := strings.TrimLeft(r.Path(), "/")
	if path == "." {
		return ""
	}
	return path
}

// Ext 获取当前 URL 最后一个路径段的后缀，不含点号。
func (r *Request) Ext() string {
	path := r.Pathinfo()
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

// Param 按 ThinkPHP 的 route、get、body 合并顺序读取，后合并的请求体优先级最高。
func (r *Request) Param(key string, defaults ...string) string {
	value, ok := r.paramValue(key)
	return r.filteredRequestScalar(value, ok, defaults)
}

// Get 获取查询参数，显式空值不会被默认值覆盖。
func (r *Request) Get(key string, defaults ...string) string {
	value, ok := r.getValue(key)
	return r.filteredRequestScalar(value, ok, defaults)
}

// Post 获取表单或 JSON 请求体中的标量参数，显式空值不会被默认值覆盖。
func (r *Request) Post(key string, defaults ...string) string {
	value, ok := r.postValue(key)
	return r.filteredRequestScalar(value, ok, defaults)
}

// Route 仅从路由和中间件透传数据中读取标量参数。
func (r *Request) Route(key string, defaults ...string) string {
	value, ok := r.RouteValue(key)
	return r.filteredRequestScalar(value, ok, defaults)
}

// RouteValue 返回路由变量及其存在状态，并保留原始类型供动作参数绑定。
func (r *Request) RouteValue(key string) (interface{}, bool) {
	if r == nil {
		return nil, false
	}
	r.routeMu.RLock()
	value, ok := r.routeParams[key]
	r.routeMu.RUnlock()
	if !ok {
		return nil, false
	}
	return deepCloneRequestValue(value), true
}

// All 返回参数快照，嵌套 JSON、切片和字节数据均使用防御性副本。
func (r *Request) All() map[string]interface{} {
	result := make(map[string]interface{})
	if r != nil {
		r.routeMu.RLock()
		for key, value := range r.routeParams {
			result[key] = deepCloneRequestValue(value)
		}
		r.routeMu.RUnlock()
	}
	if values, configured := r.getSnapshot(); configured {
		for key, value := range values {
			result[key] = value
		}
	} else {
		for key, values := range r.queryValues() {
			result[key] = normalizeStringSliceValue(values)
		}
	}

	method := r.Method(true)
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodDelete || method == http.MethodPatch {
		if values, configured := r.postSnapshot(); configured {
			for key, value := range values {
				result[key] = value
			}
		} else {
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
		}
	}
	return r.filteredRequestMap(result)
}

// Only 返回指定字段的参数快照，缺失字段使用空字符串保持原 API 语义。
func (r *Request) Only(keys ...string) map[string]interface{} {
	result := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		if value, ok := r.paramValue(key); ok {
			result[key] = r.filteredRequestValue(value)
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
		result = append(result, r.applyRequestFilters(text))
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
	if input, configured := r.inputOverride(); configured {
		if int64(len(input)) > r.maxBodyBytes {
			return fmt.Errorf("%w: 上限 %d 字节", ErrRequestBodyTooLarge, r.maxBodyBytes)
		}
		return nil
	}
	r.errorMu.RLock()
	defer r.errorMu.RUnlock()
	return r.bodyErr
}

// Parse 根据 Content-Type 预解析结构化请求体，用于在业务逻辑前阻断非法输入。
func (r *Request) Parse() error {
	return r.parseBody(true)
}

// ValidateBody 在业务执行前严格校验正文，JSON 参数树延迟到 Parse 或参数读取时创建。
// 正文限制、重复键、对象根和深度约束与 Parse 相同；原始流仍恢复给原文处理器。
func (r *Request) ValidateBody() error {
	return r.parseBody(false)
}

func (r *Request) parseBody(materialize bool) error {
	if r == nil {
		return nil
	}
	if _, err, overridden := r.inputOverrideState(materialize); overridden {
		return err
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
	// JSON 无论是否为空都需要严格校验；先同步正文缓存，避免并发读取 Body 恢复状态。
	if isJSONMediaType(mediaType) {
		r.prepareJSONBody(materialize)
		return r.JSONError()
	}
	// 没有请求体时不触发 io.ReadAll 和 ParseForm；查询参数仍由 Get/All 惰性读取。
	_, inputConfigured := r.inputOverride()
	if !r.hasOriginalBody() && !inputConfigured {
		return nil
	}
	switch {
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
	if _, err, overridden := r.inputOverrideState(false); overridden {
		return err
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
	if _, err, overridden := r.inputOverrideState(false); overridden {
		return err
	}
	r.errorMu.RLock()
	defer r.errorMu.RUnlock()
	return r.jsonErr
}

// Json 将单一且无重复键的 JSON 文档绑定到目标值。
func (r *Request) Json(target interface{}) error {
	body, validated, overridden, err := r.jsonInputOverride()
	if err != nil {
		return err
	}
	if !overridden {
		body, err = r.readOriginalBody()
		if err != nil {
			return err
		}
		validated = r.isJSONValidated()
	}
	var decodeErr error
	if validated {
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
	value, exists := r.headerLookup(key)
	if exists {
		return r.applyRequestFilters(value)
	}
	return r.applyRequestFilters(firstDefault(defaults))
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

// CleanupRequiresSupervisor 返回请求是否可能持有需要收尾截止时间监督的资源。
func (r *Request) CleanupRequiresSupervisor() bool {
	return r != nil && r.cleanupNeedsSupervisor.Load()
}

func (r *Request) IsGet() bool    { return r.Method() == http.MethodGet }
func (r *Request) IsPost() bool   { return r.Method() == http.MethodPost }
func (r *Request) IsPut() bool    { return r.Method() == http.MethodPut }
func (r *Request) IsDelete() bool { return r.Method() == http.MethodDelete }
func (r *Request) IsHead() bool   { return r.Method() == http.MethodHead }
func (r *Request) IsPatch() bool  { return r.Method() == http.MethodPatch }
func (r *Request) IsOptions() bool {
	return r.Method() == http.MethodOptions
}

// IsJson 判断客户端协商的响应类型是否包含 JSON。
func (r *Request) IsJson() bool {
	accept := strings.ToLower(r.headerValue("Accept"))
	return strings.Contains(accept, "/json") || strings.Contains(accept, "+json")
}

// IsAjax 判断请求是否由 XMLHttpRequest 或默认 _ajax 参数声明。
// original=true 时只检查原始请求头。
func (r *Request) IsAjax(original ...bool) bool {
	headerMatch := strings.EqualFold(r.Header("X-Requested-With"), "XMLHttpRequest")
	if len(original) > 0 && original[0] {
		return headerMatch
	}
	return headerMatch || r.Param("_ajax") != ""
}

// IsPjax 判断请求是否由 X-PJAX 或默认 _pjax 参数声明。
func (r *Request) IsPjax(original ...bool) bool {
	headerMatch := r.Header("X-PJAX") != ""
	if len(original) > 0 && original[0] {
		return headerMatch
	}
	return headerMatch || r.Param("_pjax") != ""
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
	if r != nil && r.cleanupStarted.Load() {
		return nil, ErrRequestCleaned
	}
	if state := r.peekThinkPHPState(); state != nil {
		state.mu.RLock()
		if state.fileSet {
			file, exists := state.fileValues[key]
			state.mu.RUnlock()
			if !exists || file == nil {
				return nil, http.ErrMissingFile
			}
			return cloneMultipartFileHeader(file), nil
		}
		state.mu.RUnlock()
	}
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

// Cookie 获取 Cookie 值，缺失时使用可选默认值。
func (r *Request) Cookie(key string, defaults ...string) string {
	state := r.peekThinkPHPState()
	if state != nil {
		state.mu.RLock()
		if state.cookieSet {
			value, ok := state.cookieValues[key]
			state.mu.RUnlock()
			return r.filteredRequestScalar(value, ok, defaults)
		}
		state.mu.RUnlock()
	}
	if r != nil && r.raw != nil {
		cookie, err := r.raw.Cookie(key)
		if err == nil {
			return r.applyRequestFilters(cookie.Value)
		}
	}
	return r.applyRequestFilters(firstDefault(defaults))
}

// Has 判断参数是否存在，显式 null 或空字符串也视为存在。
func (r *Request) Has(key string) bool {
	_, ok := r.paramValue(key)
	return ok
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

// SetRoute 合并路由参数；支持 map 或名称和值两种调用方式。
func (r *Request) SetRoute(arguments ...interface{}) *Request {
	if r == nil {
		return r
	}
	values := make(map[string]interface{})
	if len(arguments) == 1 {
		if configured, ok := arguments[0].(map[string]interface{}); ok {
			values = configured
		}
	} else if len(arguments) == 2 {
		if key, ok := arguments[0].(string); ok {
			values[key] = arguments[1]
		}
	}
	r.routeMu.Lock()
	if r.routeParams == nil {
		r.routeParams = make(map[string]interface{})
	}
	for key, value := range values {
		r.routeParams[key] = deepCloneRequestValue(value)
	}
	r.routeMu.Unlock()
	return r
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

func (r *Request) readBody() ([]byte, error) {
	if r == nil {
		return []byte{}, nil
	}
	if input, configured := r.inputOverride(); configured {
		if int64(len(input)) > r.maxBodyBytes {
			return nil, fmt.Errorf("%w: 上限 %d 字节", ErrRequestBodyTooLarge, r.maxBodyBytes)
		}
		return []byte(input), nil
	}
	return r.readOriginalBody()
}

// readOriginalBody 单独缓存原始流，避免输入覆盖与绑定验证交错时混用两个正文版本。
func (r *Request) readOriginalBody() ([]byte, error) {
	if r == nil {
		return []byte{}, nil
	}
	r.bodyOnce.Do(func() {
		source := r.originalBody()
		if source == nil || source == http.NoBody {
			r.bodyCache = []byte{}
			return
		}
		if r.raw.ContentLength > r.maxBodyBytes {
			r.setBodyError(fmt.Errorf("%w: 上限 %d 字节", ErrRequestBodyTooLarge, r.maxBodyBytes))
			_ = source.Close()
			r.replaceOriginalBody(http.NoBody)
			return
		}

		body, readErr := io.ReadAll(source)
		closeErr := source.Close()
		if readErr != nil {
			r.setBodyError(normalizeBodyReadError(readErr, r.maxBodyBytes))
			r.replaceOriginalBody(http.NoBody)
			return
		}
		if closeErr != nil {
			r.setBodyError(fmt.Errorf("关闭请求体失败: %w", closeErr))
			r.replaceOriginalBody(http.NoBody)
			return
		}
		r.bodyCache = body
		r.replaceOriginalBody(io.NopCloser(bytes.NewReader(body)))
	})
	r.errorMu.RLock()
	err := r.bodyErr
	r.errorMu.RUnlock()
	return r.bodyCache, err
}

func (r *Request) ensureFormParsed() error {
	if r == nil || r.raw == nil {
		return nil
	}
	if r.cleanupStarted.Load() {
		return ErrRequestCleaned
	}
	r.formOnce.Do(func() {
		if r.cleanupStarted.Load() {
			r.setFormError(ErrRequestCleaned)
			return
		}
		mediaType, err := r.parsedMediaType()
		if err != nil {
			r.setFormError(err)
			return
		}
		switch mediaType {
		case "application/x-www-form-urlencoded":
			body, bodyErr := r.readOriginalBody()
			if bodyErr != nil {
				r.setFormError(bodyErr)
				return
			}
			if err := r.raw.ParseForm(); err != nil {
				r.setFormError(fmt.Errorf("%w: %v", ErrInvalidFormBody, err))
				return
			}
			r.replaceOriginalBody(io.NopCloser(bytes.NewReader(body)))
		case "multipart/form-data":
			r.cleanupNeedsSupervisor.Store(true)
			r.formMu.Lock()
			defer r.formMu.Unlock()
			if r.cleanupStarted.Load() || r.cleaned {
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
	method := r.Method(true)
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodDelete || method == http.MethodPatch {
		if value, exists := r.postValue(key); exists {
			return value, true
		}
	}
	if value, exists := r.getValue(key); exists {
		return value, true
	}
	r.routeMu.RLock()
	value, ok := r.routeParams[key]
	r.routeMu.RUnlock()
	if ok {
		return value, true
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

func (r *bodyLimitReadCloser) Close() error {
	return r.source.Close()
}
