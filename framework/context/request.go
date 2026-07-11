package context

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// Request 封装原生 HTTP 请求，并补齐框架层常用的参数访问能力。
// data 字段通过读写锁保护；body/json/query/form 等惰性缓存通过 sync.Once 保护，
// 使请求对象在被多个中间件/协程并发访问时仍然安全（与 data 的并发保护保持一致）。
type Request struct {
	raw                  *http.Request
	dataMu               sync.RWMutex
	data                 map[string]interface{}
	jsonBody             map[string]interface{}
	jsonOnce             sync.Once
	bodyCache            []byte
	bodyOnce             sync.Once
	bodyErr              error
	queryCache           url.Values
	queryOnce            sync.Once
	formOnce             sync.Once
	trustedProxies       []*net.IPNet
	multipartMemoryLimit int64
}

// DefaultMultipartMemoryLimit multipart 表单在内存中缓存的默认上限（32MB），
// 超出部分写入临时文件。
const DefaultMultipartMemoryLimit int64 = 32 << 20

// NewRequest 创建请求包装器。
func NewRequest(r *http.Request, options ...RequestOption) *Request {
	req := &Request{
		raw:                  r,
		data:                 make(map[string]interface{}),
		multipartMemoryLimit: DefaultMultipartMemoryLimit,
	}
	for _, option := range options {
		if option != nil {
			option(req)
		}
	}
	return req
}

// Method 获取请求方法。
func (r *Request) Method() string {
	if r.raw == nil {
		return ""
	}
	return r.raw.Method
}

// Host 获取请求主机。
func (r *Request) Host() string {
	if r.raw == nil {
		return ""
	}
	return r.raw.Host
}

// Path 获取请求路径。
func (r *Request) Path() string {
	if r.raw == nil || r.raw.URL == nil {
		return ""
	}
	return r.raw.URL.Path
}

// Pathinfo 获取请求路径信息（Path 的别名）。
// 对应 ThinkPHP 的 $request->pathinfo()
func (r *Request) Pathinfo() string {
	return r.Path()
}

// Ext 获取当前 URL 的后缀（不含点号）。
// 例如 /index.html 返回 "html"，/api/user 返回 ""。
// 对应 ThinkPHP 的 $request->ext()
func (r *Request) Ext() string {
	path := r.Path()
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i+1:]
		}
		if path[i] == '/' {
			break
		}
	}
	return ""
}

// Param 按“路由参数 > 请求体参数 > 查询参数”的优先级读取参数。
func (r *Request) Param(key string, def ...string) string {
	if value, ok := r.paramValue(key); ok {
		return stringifyRequestValue(value)
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

// queryValues 解析并缓存 URL 查询参数，避免每次参数访问都重新解析查询串。
// 通过 sync.Once 保证并发访问下只解析一次且无数据竞争。
func (r *Request) queryValues() url.Values {
	r.queryOnce.Do(func() {
		if r.raw == nil || r.raw.URL == nil {
			r.queryCache = url.Values{}
			return
		}
		r.queryCache = r.raw.URL.Query()
	})
	return r.queryCache
}

// Get 获取查询参数。
func (r *Request) Get(key string, def ...string) string {
	if r.raw == nil || r.raw.URL == nil {
		if len(def) > 0 {
			return def[0]
		}
		return ""
	}
	value := r.queryValues().Get(key)
	if value == "" && len(def) > 0 {
		return def[0]
	}
	return value
}

// Post 获取表单或 JSON 请求体中的参数。
func (r *Request) Post(key string, def ...string) string {
	if r.raw != nil {
		r.ensureFormParsed()
		if value := r.raw.PostFormValue(key); value != "" {
			return value
		}
	}

	if strings.Contains(r.Header("Content-Type"), "application/json") {
		if value := r.getJSONBodyValue(key); value != nil {
			return stringifyRequestValue(value)
		}
	}

	if len(def) > 0 {
		return def[0]
	}
	return ""
}

// Route 仅从路由透传数据中读取参数，不会回落到查询、表单或 JSON。
func (r *Request) Route(key string, def ...string) string {
	r.dataMu.RLock()
	value, ok := r.data[key]
	r.dataMu.RUnlock()
	if ok {
		return stringifyRequestValue(value)
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

// All 返回合并后的全部参数，适合做批量读取和透传。
func (r *Request) All() map[string]interface{} {
	result := make(map[string]interface{})

	if r.raw != nil && r.raw.URL != nil {
		for key, values := range r.queryValues() {
			result[key] = normalizeStringSliceValue(values)
		}
	}

	if r.raw != nil {
		r.ensureFormParsed()
		for key, values := range r.raw.PostForm {
			result[key] = normalizeStringSliceValue(values)
		}
	}

	if strings.Contains(r.Header("Content-Type"), "application/json") {
		r.parseJSONBody()
		for key, value := range r.jsonBody {
			result[key] = value
		}
	}

	r.dataMu.RLock()
	for key, value := range r.data {
		result[key] = value
	}
	r.dataMu.RUnlock()

	return result
}

// Only 只返回指定字段，缺失字段会按空字符串填充，保持兼容调用体验。
func (r *Request) Only(keys ...string) map[string]interface{} {
	result := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		if value, ok := r.paramValue(key); ok {
			result[key] = value
			continue
		}
		result[key] = ""
	}
	return result
}

// Except 返回排除指定字段后的参数集合。
func (r *Request) Except(keys ...string) map[string]interface{} {
	result := r.All()
	for _, key := range keys {
		delete(result, key)
	}
	return result
}

// ParamInt 将参数解析为 int。
func (r *Request) ParamInt(key string, def int) int {
	value, ok := r.paramValue(key)
	if !ok {
		return def
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(stringifyRequestValue(value)))
	if err != nil {
		return def
	}
	return parsed
}

// ParamInt64 将参数解析为 int64。
func (r *Request) ParamInt64(key string, def int64) int64 {
	value, ok := r.paramValue(key)
	if !ok {
		return def
	}
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int8:
		return int64(typed)
	case int16:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case float32:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed
		}
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(stringifyRequestValue(value)), 10, 64)
	if err != nil {
		return def
	}
	return parsed
}

// ParamFloat 将参数解析为 float64。
func (r *Request) ParamFloat(key string, def float64) float64 {
	value, ok := r.paramValue(key)
	if !ok {
		return def
	}
	switch typed := value.(type) {
	case float32:
		return float64(typed)
	case float64:
		return typed
	case int:
		return float64(typed)
	case int8:
		return float64(typed)
	case int16:
		return float64(typed)
	case int32:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			return parsed
		}
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(stringifyRequestValue(value)), 64)
	if err != nil {
		return def
	}
	return parsed
}

// ParamBool 将参数解析为 bool，兼容 true/false、1/0、yes/no 等常见写法。
func (r *Request) ParamBool(key string, def bool) bool {
	value, ok := r.paramValue(key)
	if !ok {
		return def
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case int:
		return typed != 0
	case int8:
		return typed != 0
	case int16:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case float32:
		return typed != 0
	case float64:
		return typed != 0
	}

	text := strings.TrimSpace(strings.ToLower(stringifyRequestValue(value)))
	switch text {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	default:
		return def
	}
}

// PostArray 从 JSON 请求体中提取字符串数组。
func (r *Request) PostArray(key string) []string {
	if !strings.Contains(r.Header("Content-Type"), "application/json") {
		return nil
	}

	value := r.getJSONBodyValue(key)
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			result = append(result, stringifyRequestValue(item))
		}
		return result
	default:
		return nil
	}
}

// Body 返回原始请求体。
func (r *Request) Body() ([]byte, error) {
	return r.readBody(), r.bodyErr
}

// BodyReadError 返回已经发生的请求体读取错误，不会主动读取请求体。
func (r *Request) BodyReadError() error {
	return r.bodyErr
}

// Json 将 JSON 请求体绑定到目标结构体。
func (r *Request) Json(target interface{}) error {
	body := r.readBody()
	if r.bodyErr != nil {
		return r.bodyErr
	}
	return json.Unmarshal(body, target)
}

// Header 获取请求头。
func (r *Request) Header(key string, def ...string) string {
	if r.raw == nil {
		if len(def) > 0 {
			return def[0]
		}
		return ""
	}
	value := r.raw.Header.Get(key)
	if value == "" && len(def) > 0 {
		return def[0]
	}
	return value
}

// Raw 返回原始请求对象。
func (r *Request) Raw() *http.Request {
	return r.raw
}

// IsGet 判断是否为 GET 请求。
func (r *Request) IsGet() bool {
	return r.Method() == http.MethodGet
}

// IsPost 判断是否为 POST 请求。
func (r *Request) IsPost() bool {
	return r.Method() == http.MethodPost
}

// IsPut 判断是否为 PUT 请求。
func (r *Request) IsPut() bool {
	return r.Method() == http.MethodPut
}

// IsDelete 判断是否为 DELETE 请求。
func (r *Request) IsDelete() bool {
	return r.Method() == http.MethodDelete
}

// IsAjax 判断是否为 AJAX 请求。
func (r *Request) IsAjax() bool {
	return r.Header("X-Requested-With") == "XMLHttpRequest"
}

// Ip 获取客户端 IP。
func (r *Request) Ip() string {
	if r.raw == nil {
		return ""
	}
	return resolveClientIP(r.raw, r.trustedProxies)
}

// Input 是 Param 的别名，用于兼容旧调用习惯。
func (r *Request) Input(key string, def ...string) string {
	return r.Param(key, def...)
}

// File 获取上传文件。
func (r *Request) File(key string) (*multipart.FileHeader, error) {
	if r.raw == nil {
		return nil, fmt.Errorf("request is nil")
	}
	if err := r.raw.ParseMultipartForm(r.multipartMemoryLimit); err != nil {
		return nil, err
	}
	file, header, err := r.raw.FormFile(key)
	if err != nil {
		return nil, err
	}
	_ = file.Close()
	return header, nil
}

// Cleanup 释放 multipart 解析产生的临时文件。
func (r *Request) Cleanup() {
	if r.raw == nil || r.raw.MultipartForm == nil {
		return
	}
	_ = r.raw.MultipartForm.RemoveAll()
	r.raw.MultipartForm = nil
}

// Cookie 获取 Cookie 值。
func (r *Request) Cookie(key string) string {
	if r.raw == nil {
		return ""
	}
	cookie, err := r.raw.Cookie(key)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// Has 判断参数是否存在。
func (r *Request) Has(key string) bool {
	_, ok := r.paramValue(key)
	return ok
}

// IsSsl 判断是否为 HTTPS 请求。
func (r *Request) IsSsl() bool {
	if r.raw == nil {
		return false
	}
	return isHTTPS(r.raw, r.trustedProxies)
}

// Scheme 获取请求协议。
func (r *Request) Scheme() string {
	if r.IsSsl() {
		return "https"
	}
	return "http"
}

// Port 获取请求端口。
func (r *Request) Port() string {
	if r.raw == nil {
		if r.IsSsl() {
			return "443"
		}
		return "80"
	}
	_, port, err := net.SplitHostPort(r.raw.Host)
	if err == nil {
		return port
	}
	if r.IsSsl() {
		return "443"
	}
	return "80"
}

// Url 获取请求 URL，可选返回完整 URL。
func (r *Request) Url(complete ...bool) string {
	if r.raw == nil || r.raw.URL == nil {
		return ""
	}
	if len(complete) > 0 && complete[0] {
		return r.Scheme() + "://" + r.Host() + r.raw.URL.String()
	}
	return r.raw.URL.String()
}

// BaseUrl 获取不带查询字符串的完整地址。
func (r *Request) BaseUrl() string {
	if r.raw == nil || r.raw.URL == nil {
		return ""
	}
	return r.Scheme() + "://" + r.Host() + r.raw.URL.Path
}

// Root 获取站点根地址。
func (r *Request) Root() string {
	return r.Scheme() + "://" + r.Host()
}

// ContentType 获取请求内容类型。
func (r *Request) ContentType() string {
	contentType := r.Header("Content-Type")
	if contentType == "" {
		return "application/x-www-form-urlencoded"
	}
	return contentType
}

// Server 为兼容旧接口，继续从请求头读取对应键值。
func (r *Request) Server(key string) string {
	return r.Header(key)
}

// Set 写入中间件或路由透传数据。
func (r *Request) Set(key string, value interface{}) {
	r.dataMu.Lock()
	r.data[key] = value
	r.dataMu.Unlock()
}

// GetData 获取透传数据。
func (r *Request) GetData(key string) interface{} {
	r.dataMu.RLock()
	defer r.dataMu.RUnlock()
	return r.data[key]
}

// readBody 读取并缓存请求体，同时把原始 Body 复原给后续逻辑继续消费。
// 通过 sync.Once 保证并发访问下只读取一次且无数据竞争。
func (r *Request) readBody() []byte {
	r.bodyOnce.Do(func() {
		if r.raw == nil || r.raw.Body == nil {
			r.bodyCache = []byte{}
			return
		}

		body, err := io.ReadAll(r.raw.Body)
		if err != nil {
			r.bodyErr = err
			r.bodyCache = []byte{}
			return
		}

		r.bodyCache = body
		r.raw.Body = io.NopCloser(bytes.NewReader(body))
	})
	return r.bodyCache
}

// parseJSONBody 只解析一次 JSON 请求体，并缓存结果。
// 通过 sync.Once 保证并发访问下只解析一次且无数据竞争。
func (r *Request) parseJSONBody() {
	r.jsonOnce.Do(func() {
		r.jsonBody = make(map[string]interface{})

		body := r.readBody()
		if r.bodyErr != nil {
			return
		}
		if len(body) == 0 {
			return
		}

		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if err := decoder.Decode(&r.jsonBody); err != nil {
			r.jsonBody = make(map[string]interface{})
		}
	})
}

// ensureFormParsed 解析表单参数，并保证只执行一次（并发安全）。
// 对 urlencoded 表单先缓存并复原请求体，避免 ParseForm 消费 body 后 Body()/Json() 读到空；
// multipart 表单交由 File()/ParseMultipartForm 流式处理，不在此整体缓冲，以免大文件被读入内存。
func (r *Request) ensureFormParsed() {
	if r.raw == nil {
		return
	}
	r.formOnce.Do(func() {
		if strings.Contains(r.Header("Content-Type"), "application/x-www-form-urlencoded") {
			r.readBody()
		}
		_ = r.raw.ParseForm()
	})
}

// getJSONBodyValue 从缓存的 JSON 请求体中提取值。
func (r *Request) getJSONBodyValue(key string) interface{} {
	r.parseJSONBody()
	if r.jsonBody == nil {
		return nil
	}
	return r.jsonBody[key]
}

// paramValue 统一按路由参数 > 表单/JSON 请求体 > 查询参数的优先级取值。
func (r *Request) paramValue(key string) (interface{}, bool) {
	r.dataMu.RLock()
	if value, ok := r.data[key]; ok {
		r.dataMu.RUnlock()
		return value, true
	}
	r.dataMu.RUnlock()

	if r.raw != nil {
		r.ensureFormParsed()
		if values, ok := r.raw.PostForm[key]; ok && len(values) > 0 {
			return normalizeStringSliceValue(values), true
		}
	}

	if strings.Contains(r.Header("Content-Type"), "application/json") {
		r.parseJSONBody()
		if r.bodyErr != nil {
			return nil, false
		}
		if value, ok := r.jsonBody[key]; ok {
			return value, true
		}
	}

	if r.raw != nil && r.raw.URL != nil {
		if values, ok := r.queryValues()[key]; ok && len(values) > 0 {
			return normalizeStringSliceValue(values), true
		}
	}

	return nil, false
}

// normalizeStringSliceValue 把表单和查询字符串中的多值结果规范成单值或切片。
func normalizeStringSliceValue(values []string) interface{} {
	if len(values) == 1 {
		return values[0]
	}
	return append([]string(nil), values...)
}

// stringifyRequestValue 把各种可能的参数值稳定地转成字符串。
func stringifyRequestValue(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []string:
		return strings.Join(typed, ",")
	case json.Number:
		return typed.String()
	case float64:
		return formatFloat(typed)
	case float32:
		return formatFloat(float64(typed))
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(typed)
	}
}

// formatFloat 把 JSON 数值稳定转换为字符串，避免 1 被格式化成 1e+00。
func formatFloat(value float64) string {
	if value == float64(int64(value)) {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}
