package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/httpheader"
)

const (
	defaultCorsMaxAgeSeconds = 24 * 60 * 60
	corsAuthorizationHeader  = "Authorization"
)

var (
	// ErrInvalidCorsConfig 表示 CORS 配置无法形成确定且符合浏览器协议的策略。
	ErrInvalidCorsConfig = errors.New("CORS 配置非法")
)

// CorsConfig CORS 跨域配置
// 对应 ThinkPHP 8 的 cors 配置项
type CorsConfig struct {
	AllowOrigins     []string // 允许的域名列表，["*"] 表示允许所有
	AllowMethods     []string // 允许的 HTTP 方法
	AllowHeaders     []string // 允许的请求头
	ExposeHeaders    []string // 允许暴露的响应头
	AllowCredentials bool     // 是否允许携带凭证（Cookie 等）
	MaxAge           int      // 预检请求缓存时间（秒）
}

// DefaultCorsConfig 返回默认的 CORS 配置
func DefaultCorsConfig() CorsConfig {
	return CorsConfig{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"},
		AllowHeaders:     []string{"Content-Type", "Authorization", "X-Requested-With", "Accept", "Accept-Language", "Content-Language", "Origin", "token"},
		ExposeHeaders:    []string{},
		AllowCredentials: false,
		MaxAge:           defaultCorsMaxAgeSeconds,
	}
}

// Cors 返回 CORS 跨域中间件（使用默认配置）
func Cors() Handler {
	handler, err := NewCors(DefaultCorsConfig())
	if err != nil {
		return unavailableCorsHandler
	}
	return handler
}

// normalizeCorsConfig 校验并复制 CORS 配置，避免运行期间被调用方修改切片导致安全策略漂移。
func normalizeCorsConfig(config CorsConfig) (CorsConfig, error) {
	if config.MaxAge < 0 {
		return CorsConfig{}, fmt.Errorf("%w: MaxAge 不能为负数", ErrInvalidCorsConfig)
	}
	config.AllowOrigins = normalizeCorsOrigins(config.AllowOrigins)
	config.AllowMethods = normalizeCorsMethods(config.AllowMethods)
	config.AllowHeaders = normalizeCorsValues(config.AllowHeaders)
	config.ExposeHeaders = normalizeCorsValues(config.ExposeHeaders)
	if len(config.AllowOrigins) == 0 {
		return CorsConfig{}, fmt.Errorf("%w: AllowOrigins 不能为空", ErrInvalidCorsConfig)
	}
	if len(config.AllowMethods) == 0 {
		return CorsConfig{}, fmt.Errorf("%w: AllowMethods 不能为空", ErrInvalidCorsConfig)
	}
	wildcard := false
	initialOrigins := append([]string(nil), config.AllowOrigins...)
	seenExact := make(map[string]struct{}, len(initialOrigins)*2)
	for _, item := range initialOrigins {
		seenExact[item] = struct{}{}
	}
	for _, origin := range initialOrigins {
		if origin == "*" {
			wildcard = true
			continue
		}
		canonical, err := validateAllowedOrigin(origin)
		if err != nil {
			return CorsConfig{}, err
		}
		if _, exists := seenExact[canonical]; !exists {
			seenExact[canonical] = struct{}{}
			config.AllowOrigins = append(config.AllowOrigins, canonical)
		}
	}
	if wildcard && len(initialOrigins) != 1 {
		return CorsConfig{}, fmt.Errorf("%w: AllowOrigins 不能同时包含 * 和具体来源", ErrInvalidCorsConfig)
	}
	if wildcard && config.AllowCredentials {
		return CorsConfig{}, fmt.Errorf("%w: AllowCredentials=true 时不能使用 * 来源", ErrInvalidCorsConfig)
	}
	if config.AllowCredentials && (corsContainsWildcard(config.AllowMethods) || corsContainsWildcard(config.AllowHeaders) || corsContainsWildcard(config.ExposeHeaders)) {
		return CorsConfig{}, fmt.Errorf("%w: AllowCredentials=true 时方法、请求头和暴露头不能使用 *", ErrInvalidCorsConfig)
	}
	allValues := append(append(append([]string{}, config.AllowMethods...), config.AllowHeaders...), config.ExposeHeaders...)
	for _, value := range allValues {
		if value != "*" && !isCorsToken(value) {
			return CorsConfig{}, fmt.Errorf("%w: CORS 列表包含无效值: %q", ErrInvalidCorsConfig, value)
		}
	}
	return config, nil
}

func normalizeCorsOrigins(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func validateAllowedOrigin(origin string) (string, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.HasSuffix(origin, "#") {
		return "", fmt.Errorf("%w: AllowOrigins 包含无效来源: %q", ErrInvalidCorsConfig, origin)
	}
	canonical := canonicalBrowserOrigin(parsed)
	if canonical == "" {
		return "", fmt.Errorf("%w: AllowOrigins 包含无效来源: %q", ErrInvalidCorsConfig, origin)
	}
	return canonical, nil
}

// canonicalBrowserOrigin 生成同一 HTTP(S) 来源的浏览器规范写法（含 RFC 5952 IPv6 压缩格式）。
func canonicalBrowserOrigin(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	defaultPort := defaultHTTPOriginPort(scheme)
	host := parsed.Hostname()
	if defaultPort == "" || host == "" || !utf8.ValidString(host) {
		return ""
	}
	rawAuthority := host
	canonicalAuthority := ""
	if strings.HasPrefix(parsed.Host, "[") {
		address, err := netip.ParseAddr(host)
		if err != nil || !address.Is6() || address.Zone() != "" {
			return ""
		}
		rawAuthority = "[" + host + "]"
		canonicalAuthority = "[" + strings.ToLower(address.String()) + "]"
	} else {
		canonicalAuthority = canonicalCorsDomain(host)
		if canonicalAuthority == "" {
			return ""
		}
	}
	rawPort := parsed.Port()
	if rawPort == "" {
		if parsed.Host != rawAuthority {
			return ""
		}
	} else {
		if parsed.Host != rawAuthority+":"+rawPort {
			return ""
		}
		port, err := strconv.ParseUint(rawPort, 10, 16)
		if err != nil || port == 0 {
			return ""
		}
		if normalized := strconv.FormatUint(port, 10); normalized != defaultPort {
			canonicalAuthority += ":" + normalized
		}
	}
	return scheme + "://" + canonicalAuthority
}

func isCorsASCIIHostCharacter(char rune) bool {
	return char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' ||
		char >= '0' && char <= '9' || char == '.' || char == '-' || char == '_'
}

func normalizeCorsMethods(values []string) []string {
	normalized := normalizeCorsValues(values)
	for index, value := range normalized {
		if value != "*" {
			normalized[index] = strings.ToUpper(value)
		}
	}
	return normalized
}

func corsContainsWildcard(values []string) bool {
	for _, value := range values {
		if value == "*" {
			return true
		}
	}
	return false
}

func normalizeCorsValues(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func isCorsToken(value string) bool {
	for _, char := range value {
		if char <= 0x20 || char >= 0x7f {
			return false
		}
		switch char {
		case '(', ')', '<', '>', '@', ',', ';', ':', '\\', '"', '/', '[', ']', '?', '=', '{', '}':
			return false
		}
	}
	return value != ""
}

func corsMethodAllowed(values []string, target string) bool {
	target = strings.ToUpper(strings.TrimSpace(target))
	if !isCorsToken(target) {
		return false
	}
	for _, value := range values {
		if value == "*" || value == target {
			return true
		}
	}
	return false
}

func canonicalCorsSafelistedHeader(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "accept":
		return "Accept", true
	case "accept-language":
		return "Accept-Language", true
	case "content-language":
		return "Content-Language", true
	default:
		return "", false
	}
}

func corsHeaderAllowed(values []string, target string) bool {
	if _, ok := canonicalCorsSafelistedHeader(target); ok {
		return true
	}
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
		// Authorization 是 Fetch 规范中的非通配请求头，必须显式列出。
		if value == "*" && !strings.EqualFold(target, corsAuthorizationHeader) {
			return true
		}
	}
	return false
}

func corsRequestedHeadersAllowed(allowed []string, requested string) bool {
	if strings.TrimSpace(requested) == "" {
		return true
	}
	for _, header := range strings.Split(requested, ",") {
		header = strings.TrimSpace(header)
		if header == "" {
			continue
		}
		if !isCorsToken(header) || !corsHeaderAllowed(allowed, header) {
			return false
		}
	}
	return true
}

func corsRequestedHeaderValue(req *context.Request) string {
	if req == nil {
		return ""
	}
	if raw := req.Raw(); raw != nil {
		if values := raw.Header.Values("Access-Control-Request-Headers"); len(values) > 1 {
			return strings.Join(values, ",")
		}
	}
	return req.Header("Access-Control-Request-Headers")
}

func effectiveCorsAllowHeaders(allowed []string, baseHeaders, requested string) string {
	if corsContainsWildcard(allowed) || strings.TrimSpace(requested) == "" {
		return baseHeaders
	}
	var extras []string
	seen := make(map[string]struct{}, len(allowed)+3)
	for _, item := range allowed {
		seen[strings.ToLower(item)] = struct{}{}
	}
	for _, header := range strings.Split(requested, ",") {
		canonicalSafe, ok := canonicalCorsSafelistedHeader(header)
		if !ok {
			continue
		}
		lower := strings.ToLower(canonicalSafe)
		if _, exists := seen[lower]; exists {
			continue
		}
		seen[lower] = struct{}{}
		extras = append(extras, canonicalSafe)
	}
	if len(extras) == 0 {
		return baseHeaders
	}
	if baseHeaders == "" {
		return strings.Join(extras, ", ")
	}
	return baseHeaders + ", " + strings.Join(extras, ", ")
}

// NewCors 在应用启动阶段校验并冻结 CORS 策略。
func NewCors(config CorsConfig) (Handler, error) {
	normalized, err := normalizeCorsConfig(config)
	if err != nil {
		return nil, err
	}
	return newCorsHandler(normalized), nil
}

// CorsWithConfig 返回带自定义配置的兼容中间件。
func CorsWithConfig(config CorsConfig) Handler {
	handler, err := NewCors(config)
	if err != nil {
		return unavailableCorsHandler
	}
	return handler
}

func unavailableCorsHandler(*context.Request, func(*context.Request) *context.Response) *context.Response {
	return context.NewResponse().Code(http.StatusInternalServerError).Content("invalid CORS configuration")
}

func newCorsHandler(config CorsConfig) Handler {
	// 预计算稳定响应值，避免每次请求重复拼接配置切片。
	allowMethods := strings.Join(config.AllowMethods, ", ")
	allowHeaders := strings.Join(config.AllowHeaders, ", ")
	exposeHeaders := strings.Join(config.ExposeHeaders, ", ")
	maxAge := ""
	if config.MaxAge > 0 {
		maxAge = strconv.Itoa(config.MaxAge)
	}
	isWildcard := len(config.AllowOrigins) == 1 && config.AllowOrigins[0] == "*"

	return func(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
		if req == nil || next == nil {
			return context.NewResponse().Code(http.StatusInternalServerError).Content(http.StatusText(http.StatusInternalServerError))
		}
		origin := strings.TrimSpace(req.Header("Origin"))
		requestedMethod := strings.TrimSpace(req.Header("Access-Control-Request-Method"))
		if req.Method() == http.MethodOptions && origin != "" && requestedMethod != "" {
			return corsPreflightResponse(config, isWildcard, origin, requestedMethod, corsRequestedHeaderValue(req), allowMethods, allowHeaders, maxAge)
		}

		// 具体来源策略的所有响应都必须按 Origin 分区，包括没有 Origin 或未命中的请求。
		if !isWildcard {
			addCorsWriterVary(req, "Origin")
		}
		if origin == "" {
			response := next(req)
			if response != nil && !isWildcard {
				addVaryValue(response, "Origin")
			}
			return response
		}

		allowOrigin := corsAllowedOrigin(config.AllowOrigins, isWildcard, origin)
		if allowOrigin == "" {
			response := next(req)
			if response != nil && !isWildcard {
				addVaryValue(response, "Origin")
			}
			return response
		}

		// 静态文件和标准库 Handler 可能在返回前提交响应，因此必须先写原生 Writer。
		addCorsActualWriterHeaders(req, allowOrigin, exposeHeaders, config.AllowCredentials, !isWildcard)
		response := next(req)
		if response == nil {
			response = context.NewResponse().Code(http.StatusNoContent)
		}
		addCorsActualResponseHeaders(response, allowOrigin, exposeHeaders, config.AllowCredentials, !isWildcard)
		return response
	}
}

func corsAllowedOrigin(allowed []string, wildcard bool, origin string) string {
	if wildcard {
		return "*"
	}
	for _, candidate := range allowed {
		if candidate == origin {
			return origin
		}
	}
	if canonical, err := validateAllowedOrigin(origin); err == nil {
		for _, candidate := range allowed {
			if candidate == canonical {
				return origin
			}
		}
	}
	return ""
}

func corsPreflightResponse(config CorsConfig, wildcard bool, origin, requestedMethod, requestedHeaders, methods, headers, maxAge string) *context.Response {
	response := context.NewResponse()
	if !wildcard {
		addVaryValue(response, "Origin")
	}
	addVaryValue(response, "Access-Control-Request-Method")
	addVaryValue(response, "Access-Control-Request-Headers")

	allowOrigin := corsAllowedOrigin(config.AllowOrigins, wildcard, origin)
	if allowOrigin == "" || !corsMethodAllowed(config.AllowMethods, requestedMethod) || !corsRequestedHeadersAllowed(config.AllowHeaders, requestedHeaders) {
		return response.Code(http.StatusForbidden)
	}
	response.Code(http.StatusNoContent)
	response.Header("Access-Control-Allow-Origin", allowOrigin)
	response.Header("Access-Control-Allow-Methods", methods)
	if effectiveHeaders := effectiveCorsAllowHeaders(config.AllowHeaders, headers, requestedHeaders); effectiveHeaders != "" {
		response.Header("Access-Control-Allow-Headers", effectiveHeaders)
	}
	if maxAge != "" {
		response.Header("Access-Control-Max-Age", maxAge)
	}
	if config.AllowCredentials {
		response.Header("Access-Control-Allow-Credentials", "true")
	}
	return response
}

func addCorsActualResponseHeaders(response *context.Response, origin, expose string, credentials, varyOrigin bool) {
	if response == nil {
		return
	}
	response.Header("Access-Control-Allow-Origin", origin)
	if expose != "" {
		response.Header("Access-Control-Expose-Headers", expose)
	}
	if credentials {
		response.Header("Access-Control-Allow-Credentials", "true")
	}
	if varyOrigin {
		addVaryValue(response, "Origin")
	}
}

func addCorsActualWriterHeaders(req *context.Request, origin, expose string, credentials, varyOrigin bool) {
	writer, exists := req.ResponseWriter()
	if !exists {
		return
	}
	headers := writer.Header()
	headers.Set("Access-Control-Allow-Origin", origin)
	if expose != "" {
		headers.Set("Access-Control-Expose-Headers", expose)
	}
	if credentials {
		headers.Set("Access-Control-Allow-Credentials", "true")
	}
	if varyOrigin {
		addHTTPVaryValue(headers, "Origin")
	}
}

func addCorsWriterVary(req *context.Request, value string) {
	if req == nil {
		return
	}
	if writer, exists := req.ResponseWriter(); exists {
		addHTTPVaryValue(writer.Header(), value)
	}
}

// addVaryValue 合并既有 Vary 值，避免 CORS 中间件覆盖压缩或语言协商策略。
func addVaryValue(resp *context.Response, value string) {
	if resp == nil || strings.TrimSpace(value) == "" {
		return
	}
	resp.Header("Vary", httpheader.MergeVary(resp.Headers().Values("Vary"), []string{value}))
}

// addHTTPVaryValue 在原生响应写出前合并缓存协商维度。
func addHTTPVaryValue(headers http.Header, value string) {
	if headers == nil || strings.TrimSpace(value) == "" {
		return
	}
	headers.Set("Vary", httpheader.MergeVary(headers.Values("Vary"), []string{value}))
}
