package context

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	frameworkenv "github.com/zhuhanxin0308/thinkgo/v3/env"
	frameworksession "github.com/zhuhanxin0308/thinkgo/v3/session"
)

const (
	defaultRequestTokenName = "__token__"
	requestTokenBytes       = 32
)

// RequestFilter 是 Go 对 ThinkPHP 请求过滤回调的强类型表达。
type RequestFilter func(string) string

type requestMIMEType struct {
	name   string
	values []string
}

type requestThinkPHPState struct {
	mu sync.RWMutex

	createdAt time.Time

	getValues    map[string]interface{}
	getSet       bool
	postValues   map[string]interface{}
	postSet      bool
	cookieValues map[string]interface{}
	cookieSet    bool
	serverValues map[string]interface{}
	serverSet    bool
	headerValues http.Header
	headerSet    bool
	fileValues   map[string]*multipart.FileHeader
	fileSet      bool
	inputValue   string
	inputSet     bool
	inputParsed  bool
	inputMedia   string
	inputLimit   int64
	inputValues  map[string]interface{}
	inputErr     error

	session   *frameworksession.Session
	rule      interface{}
	filters   []RequestFilter
	mimeTypes []requestMIMEType

	rootDomain  string
	subDomain   string
	panDomain   string
	baseFile    string
	baseFileSet bool
	secureKey   string
	tokenErr    error
}

// defaultRequestMIMETypes 只在初始化时创建；请求首次自定义时复制外层表，后续仅替换自有条目。
// 条目中的值切片始终只读，避免普通请求反复分配默认 MIME 定义。
var defaultRequestMIMETypes = [...]requestMIMEType{
	{name: "xml", values: []string{"application/xml", "text/xml", "application/x-xml"}},
	{name: "json", values: []string{"application/json", "text/x-json", "application/jsonrequest", "text/json"}},
	{name: "js", values: []string{"text/javascript", "application/javascript", "application/x-javascript"}},
	{name: "css", values: []string{"text/css"}},
	{name: "rss", values: []string{"application/rss+xml"}},
	{name: "yaml", values: []string{"application/x-yaml", "text/yaml"}},
	{name: "atom", values: []string{"application/atom+xml"}},
	{name: "pdf", values: []string{"application/pdf"}},
	{name: "text", values: []string{"text/plain"}},
	{name: "image", values: []string{"image/png", "image/jpg", "image/jpeg", "image/pjpeg", "image/gif", "image/webp", "image/*"}},
	{name: "csv", values: []string{"text/csv"}},
	{name: "html", values: []string{"text/html", "application/xhtml+xml", "*/*"}},
}

func (r *Request) thinkPHPState() *requestThinkPHPState {
	if r == nil {
		return nil
	}
	r.compatibilityOnce.Do(func() {
		createdAt := r.createdAt
		if createdAt.IsZero() {
			// 零值 Request 在首次兼容调用时确定时间，保持既有语义。
			createdAt = time.Now()
		}
		r.compatibility.Store(&requestThinkPHPState{createdAt: createdAt})
	})
	return r.compatibility.Load()
}

// peekThinkPHPState 只观察已发布状态，让普通读取避免创建无覆盖值的兼容对象。
func (r *Request) peekThinkPHPState() *requestThinkPHPState {
	if r == nil {
		return nil
	}
	return r.compatibility.Load()
}

// WithGet 替换 GET 数据，供请求工厂和测试按 ThinkPHP 初始化顺序装配请求。
func (r *Request) WithGet(values map[string]interface{}) *Request {
	state := r.thinkPHPState()
	if state == nil {
		return r
	}
	state.mu.Lock()
	state.getValues = cloneRequestMap(values)
	state.getSet = true
	state.mu.Unlock()
	return r
}

// WithPost 替换 POST 数据。
func (r *Request) WithPost(values map[string]interface{}) *Request {
	state := r.thinkPHPState()
	if state == nil {
		return r
	}
	state.mu.Lock()
	state.postValues = cloneRequestMap(values)
	state.postSet = true
	state.mu.Unlock()
	return r
}

// WithCookie 替换 Cookie 数据。
func (r *Request) WithCookie(values map[string]interface{}) *Request {
	state := r.thinkPHPState()
	if state == nil {
		return r
	}
	state.mu.Lock()
	state.cookieValues = cloneRequestMap(values)
	state.cookieSet = true
	state.mu.Unlock()
	return r
}

// SetCookie 更新当前请求可见的单个 Cookie。
func (r *Request) SetCookie(name string, value interface{}) {
	state := r.thinkPHPState()
	if state == nil || strings.TrimSpace(name) == "" {
		return
	}
	state.mu.Lock()
	if !state.cookieSet {
		state.cookieValues = r.rawCookieSnapshot()
		state.cookieSet = true
	}
	state.cookieValues[name] = deepCloneRequestValue(value)
	state.mu.Unlock()
}

// WithServer 替换 SERVER 数据，键名按 ThinkPHP 规则统一为大写。
func (r *Request) WithServer(values map[string]interface{}) *Request {
	state := r.thinkPHPState()
	if state == nil {
		return r
	}
	normalized := make(map[string]interface{}, len(values))
	for key, value := range values {
		normalized[strings.ToUpper(strings.TrimSpace(key))] = deepCloneRequestValue(value)
	}
	state.mu.Lock()
	state.serverValues = normalized
	state.serverSet = true
	state.baseFile = ""
	state.baseFileSet = false
	state.mu.Unlock()
	return r
}

// WithHeader 替换请求头数据，读取时保持 HTTP 头名大小写不敏感。
func (r *Request) WithHeader(values map[string]string) *Request {
	state := r.thinkPHPState()
	if state == nil {
		return r
	}
	headers := make(http.Header, len(values))
	for key, value := range values {
		headers.Set(key, value)
	}
	state.mu.Lock()
	state.headerValues = headers
	state.headerSet = true
	state.mu.Unlock()
	return r
}

// WithFiles 替换上传文件集合，并在读取时继续返回防御性副本。
func (r *Request) WithFiles(values map[string]*multipart.FileHeader) *Request {
	state := r.thinkPHPState()
	if state == nil {
		return r
	}
	files := make(map[string]*multipart.FileHeader, len(values))
	for key, value := range values {
		files[key] = cloneMultipartFileHeader(value)
	}
	state.mu.Lock()
	state.fileValues = files
	state.fileSet = true
	state.mu.Unlock()
	return r
}

// WithInput 替换原始输入；可识别的 JSON 或表单数据同时替换 POST/PUT 数据。
func (r *Request) WithInput(input string) *Request {
	state := r.thinkPHPState()
	if state == nil {
		return r
	}
	state.mu.Lock()
	state.inputValue = input
	state.inputSet = true
	state.inputParsed = false
	state.inputValues = nil
	state.inputErr = nil
	state.postValues = nil
	state.postSet = false
	state.mu.Unlock()
	return r
}

// WithRoute 替换 ROUTE 数据；SetRoute 用于在此基础上合并增量数据。
func (r *Request) WithRoute(values map[string]interface{}) *Request {
	if r == nil {
		return r
	}
	r.routeMu.Lock()
	r.routeParams = cloneRequestMap(values)
	r.routeMu.Unlock()
	return r
}

// WithMiddleware 合并中间件传递的数据。
func (r *Request) WithMiddleware(values map[string]interface{}) *Request {
	for key, value := range values {
		r.Set(key, value)
	}
	return r
}

// Middleware 获取中间件传递数据；无参数时返回防御性快照。
func (r *Request) Middleware(arguments ...interface{}) interface{} {
	if r == nil {
		return nil
	}
	r.dataMu.RLock()
	defer r.dataMu.RUnlock()
	if len(arguments) == 0 {
		return cloneRequestMap(r.data)
	}
	name, ok := arguments[0].(string)
	if !ok {
		return nil
	}
	if value, exists := r.data[name]; exists {
		return deepCloneRequestValue(value)
	}
	if len(arguments) > 1 {
		return deepCloneRequestValue(arguments[1])
	}
	return nil
}

// WithEnv 绑定应用环境变量服务。
func (r *Request) WithEnv(environment *frameworkenv.Env) *Request {
	if r == nil {
		return r
	}
	r.environment.Store(environment)
	return r
}

// WithEnvService 将应用 Env 注入每个新 Request，业务代码无需自行解析容器。
func WithEnvService(environment *frameworkenv.Env) RequestOption {
	return func(request *Request) error {
		request.WithEnv(environment)
		return nil
	}
}

// Env 读取环境变量；无参数时返回环境变量快照。
func (r *Request) Env(arguments ...string) interface{} {
	if r == nil {
		return nil
	}
	environment := r.environment.Load()
	if environment == nil {
		if len(arguments) > 1 {
			return arguments[1]
		}
		if len(arguments) == 0 {
			return map[string]string{}
		}
		return ""
	}
	if len(arguments) == 0 || strings.TrimSpace(arguments[0]) == "" {
		return environment.All()
	}
	return environment.Get(arguments[0], arguments[1:]...)
}

// WithSession 绑定当前请求独立的 Session，并保留旧透传键的向后兼容读取。
func (r *Request) WithSession(current *frameworksession.Session) *Request {
	state := r.thinkPHPState()
	if state == nil {
		return r
	}
	state.mu.Lock()
	state.session = current
	state.mu.Unlock()
	r.Set("_session", current)
	return r
}

// Session 读取当前请求 Session；无参数时返回全部数据快照。
func (r *Request) Session(arguments ...interface{}) interface{} {
	current := r.requestSession()
	if current == nil {
		if len(arguments) > 1 {
			return deepCloneRequestValue(arguments[1])
		}
		if len(arguments) == 0 {
			return map[string]interface{}{}
		}
		return nil
	}
	if len(arguments) == 0 {
		return current.All()
	}
	name, ok := arguments[0].(string)
	if !ok || name == "" {
		return current.All()
	}
	if value, exists := current.Get(name); exists {
		return value
	}
	if len(arguments) > 1 {
		return deepCloneRequestValue(arguments[1])
	}
	return nil
}

// SetRule 设置当前匹配的路由规则对象。
func (r *Request) SetRule(rule interface{}) *Request {
	state := r.thinkPHPState()
	if state == nil {
		return r
	}
	state.mu.Lock()
	state.rule = rule
	state.mu.Unlock()
	return r
}

// Rule 返回当前匹配的路由规则对象。
func (r *Request) Rule() interface{} {
	state := r.thinkPHPState()
	if state == nil {
		return nil
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.rule
}

// Filter 设置或获取全局请求字符串过滤器。
func (r *Request) Filter(filters ...RequestFilter) []RequestFilter {
	state := r.thinkPHPState()
	if state == nil {
		return nil
	}
	state.mu.Lock()
	if len(filters) > 0 {
		state.filters = append([]RequestFilter(nil), filters...)
	}
	result := append([]RequestFilter(nil), state.filters...)
	state.mu.Unlock()
	return result
}

// FilterValue 递归输入过滤在 Go 标量 API 中的直接入口。
func (r *Request) FilterValue(value string) string {
	return r.applyRequestFilters(value)
}

// BuildToken 使用密码学安全随机数生成并写入 Session 的表单令牌。
func (r *Request) BuildToken(names ...string) string {
	name := defaultRequestTokenName
	if len(names) > 0 && strings.TrimSpace(names[0]) != "" {
		name = names[0]
	}
	current := r.requestSession()
	if current == nil {
		r.setTokenError(errorsRequestSessionUnavailable())
		return ""
	}
	random := make([]byte, requestTokenBytes)
	if _, err := rand.Read(random); err != nil {
		r.setTokenError(err)
		return ""
	}
	token := hex.EncodeToString(random)
	if err := current.Set(name, token); err != nil {
		r.setTokenError(err)
		return ""
	}
	r.setTokenError(nil)
	return token
}

// CheckToken 验证并销毁一次性表单令牌；参数可依次指定名称和数据映射。
func (r *Request) CheckToken(arguments ...interface{}) bool {
	if r.IsGet() || r.IsHead() || r.IsOptions() {
		return true
	}
	name := defaultRequestTokenName
	var data map[string]interface{}
	if len(arguments) > 0 {
		if configured, ok := arguments[0].(string); ok && configured != "" {
			name = configured
		}
	}
	if len(arguments) > 1 {
		data, _ = arguments[1].(map[string]interface{})
	}
	current := r.requestSession()
	if current == nil {
		return false
	}
	submitted := r.headerValue("X-CSRF-TOKEN")
	if submitted == "" {
		if data == nil {
			if value, ok := r.postValue(name); ok {
				submitted, _ = stringifyRequestValue(value)
			}
		} else if value, ok := data[name]; ok {
			submitted, _ = stringifyRequestValue(value)
		}
	}
	accepted, err := current.ConsumeString(name, submitted)
	r.setTokenError(err)
	return err == nil && accepted
}

// TokenError 返回最近一次表单令牌 Session 操作错误。
func (r *Request) TokenError() error {
	state := r.thinkPHPState()
	if state == nil {
		return errorsRequestSessionUnavailable()
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.tokenErr
}

func (r *Request) requestSession() *frameworksession.Session {
	state := r.thinkPHPState()
	if state == nil {
		return nil
	}
	state.mu.RLock()
	current := state.session
	state.mu.RUnlock()
	if current != nil {
		return current
	}
	current, _ = r.GetData("_session").(*frameworksession.Session)
	return current
}

func (r *Request) setTokenError(err error) {
	state := r.thinkPHPState()
	if state == nil {
		return
	}
	state.mu.Lock()
	state.tokenErr = err
	state.mu.Unlock()
}

func errorsRequestSessionUnavailable() error {
	return fmt.Errorf("请求 Session 不可用")
}

func (r *Request) applyRequestFilters(value string) string {
	state := r.peekThinkPHPState()
	if state == nil {
		return value
	}
	state.mu.RLock()
	filters := append([]RequestFilter(nil), state.filters...)
	state.mu.RUnlock()
	for _, filter := range filters {
		if filter != nil {
			value = filter(value)
		}
	}
	return value
}

func (r *Request) filteredRequestScalar(value interface{}, exists bool, defaults []string) string {
	if exists {
		if text, valid := stringifyRequestValue(value); valid {
			return r.applyRequestFilters(text)
		}
	}
	return r.applyRequestFilters(firstDefault(defaults))
}

func (r *Request) getValue(key string) (interface{}, bool) {
	state := r.peekThinkPHPState()
	if state != nil {
		state.mu.RLock()
		if state.getSet {
			value, ok := state.getValues[key]
			state.mu.RUnlock()
			return deepCloneRequestValue(value), ok
		}
		state.mu.RUnlock()
	}
	if values, ok := r.queryValues()[key]; ok && len(values) > 0 {
		return normalizeStringSliceValue(values), true
	}
	return nil, false
}

func (r *Request) postValue(key string) (interface{}, bool) {
	state := r.peekThinkPHPState()
	if state != nil {
		state.mu.RLock()
		if state.postSet {
			value, ok := state.postValues[key]
			state.mu.RUnlock()
			return deepCloneRequestValue(value), ok
		}
		state.mu.RUnlock()
	}
	if values, err, overridden := r.parsedInputOverride(); overridden {
		if err != nil {
			return nil, false
		}
		value, exists := values[key]
		return deepCloneRequestValue(value), exists
	}
	mediaType, _ := r.parsedMediaType()
	if isFormMediaType(mediaType) {
		_ = r.ensureFormParsed()
		if r.raw != nil {
			if values, ok := r.raw.PostForm[key]; ok && len(values) > 0 {
				return normalizeStringSliceValue(values), true
			}
		}
	}
	if isJSONMediaType(mediaType) {
		return r.getJSONBodyValue(key)
	}
	return nil, false
}

func (r *Request) inputOverride() (string, bool) {
	state := r.peekThinkPHPState()
	if state == nil {
		return "", false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.inputValue, state.inputSet
}

func (r *Request) headerValue(key string) string {
	value, _ := r.headerLookup(key)
	return value
}

func (r *Request) headerLookup(key string) (string, bool) {
	state := r.peekThinkPHPState()
	if state != nil {
		state.mu.RLock()
		if state.headerSet {
			values, exists := state.headerValues[http.CanonicalHeaderKey(key)]
			state.mu.RUnlock()
			return strings.Join(values, ", "), exists
		}
		state.mu.RUnlock()
	}
	if r == nil || r.raw == nil {
		return "", false
	}
	values, exists := r.raw.Header[http.CanonicalHeaderKey(key)]
	return strings.Join(values, ", "), exists
}

func (r *Request) serverOverride(key string) (interface{}, bool) {
	state := r.peekThinkPHPState()
	if state == nil {
		return nil, false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.serverSet {
		return nil, false
	}
	value := state.serverValues[strings.ToUpper(strings.TrimSpace(key))]
	return deepCloneRequestValue(value), true
}

func (r *Request) rawCookieSnapshot() map[string]interface{} {
	result := make(map[string]interface{})
	if r == nil || r.raw == nil {
		return result
	}
	for _, current := range r.raw.Cookies() {
		if current != nil {
			result[current.Name] = current.Value
		}
	}
	return result
}

func parseFormInput(input string) (map[string]interface{}, error) {
	parsed, err := url.ParseQuery(input)
	if err != nil {
		return nil, err
	}
	result := make(map[string]interface{}, len(parsed))
	for key, values := range parsed {
		result[key] = normalizeStringSliceValue(values)
	}
	return result, nil
}

func (r *Request) getSnapshot() (map[string]interface{}, bool) {
	state := r.peekThinkPHPState()
	if state == nil {
		return nil, false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.getSet {
		return nil, false
	}
	return cloneRequestMap(state.getValues), true
}

func (r *Request) postSnapshot() (map[string]interface{}, bool) {
	state := r.peekThinkPHPState()
	if state == nil {
		return nil, false
	}
	state.mu.RLock()
	if state.postSet {
		values := cloneRequestMap(state.postValues)
		state.mu.RUnlock()
		return values, true
	}
	state.mu.RUnlock()
	values, _, overridden := r.parsedInputOverride()
	return cloneRequestMap(values), overridden
}

func (r *Request) filteredRequestValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case string:
		return r.applyRequestFilters(typed)
	case []string:
		result := make([]string, len(typed))
		for index, item := range typed {
			result[index] = r.applyRequestFilters(item)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			result[index] = r.filteredRequestValue(item)
		}
		return result
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			result[key] = r.filteredRequestValue(item)
		}
		return result
	default:
		return deepCloneRequestValue(value)
	}
}

func (r *Request) filteredRequestMap(values map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(values))
	for key, value := range values {
		result[key] = r.filteredRequestValue(value)
	}
	return result
}

func cloneRequestMap(values map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(values))
	for key, value := range values {
		result[key] = deepCloneRequestValue(value)
	}
	return result
}

func cloneMultipartFileHeader(value *multipart.FileHeader) *multipart.FileHeader {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Header = make(map[string][]string, len(value.Header))
	for key, values := range value.Header {
		cloned.Header[key] = append([]string(nil), values...)
	}
	return &cloned
}
