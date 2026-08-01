package middleware

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"thinkgo/framework/context"
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
		AllowHeaders:     []string{"Content-Type", "Authorization", "X-Requested-With", "Accept", "Origin", "token"},
		ExposeHeaders:    []string{},
		AllowCredentials: false,
		MaxAge:           86400, // 24小时
	}
}

// Cors 返回 CORS 跨域中间件（使用默认配置）
func Cors() Handler {
	return CorsWithConfig(DefaultCorsConfig())
}

// CorsWithConfig 返回带自定义配置的 CORS 中间件
// 支持域名白名单：当 AllowOrigins 不含 "*" 时，会检查请求的 Origin 是否在白名单中
// 当 AllowCredentials=true 时，不能使用 "*" 作为 Origin，会自动改为请求的 Origin

// normalizeCorsConfig 校验并复制 CORS 配置，避免运行期间被调用方修改切片导致安全策略漂移。
func normalizeCorsConfig(config CorsConfig) (CorsConfig, error) {
	if config.MaxAge < 0 {
		return CorsConfig{}, fmt.Errorf("MaxAge 不能为负数")
	}
	config.AllowOrigins = normalizeCorsValues(config.AllowOrigins)
	config.AllowMethods = normalizeCorsValues(config.AllowMethods)
	config.AllowHeaders = normalizeCorsValues(config.AllowHeaders)
	config.ExposeHeaders = normalizeCorsValues(config.ExposeHeaders)
	if len(config.AllowOrigins) == 0 {
		return CorsConfig{}, fmt.Errorf("AllowOrigins 不能为空")
	}
	wildcard := false
	for _, origin := range config.AllowOrigins {
		if origin == "*" {
			wildcard = true
			continue
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return CorsConfig{}, fmt.Errorf("AllowOrigins 包含无效来源: %q", origin)
		}
	}
	if wildcard && len(config.AllowOrigins) != 1 {
		return CorsConfig{}, fmt.Errorf("AllowOrigins 不能同时包含 * 和具体来源")
	}
	if wildcard && config.AllowCredentials {
		return CorsConfig{}, fmt.Errorf("AllowCredentials=true 时不能使用 * 来源")
	}
	allValues := append(append(append([]string{}, config.AllowMethods...), config.AllowHeaders...), config.ExposeHeaders...)
	for _, value := range allValues {
		if value != "*" && !isCorsToken(value) {
			return CorsConfig{}, fmt.Errorf("CORS 列表包含无效值: %q", value)
		}
	}
	return config, nil
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

func corsListContains(values []string, target string) bool {
	for _, value := range values {
		if value == "*" || strings.EqualFold(value, target) {
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
		if !isCorsToken(header) || !corsListContains(allowed, header) {
			return false
		}
	}
	return true
}

// CorsWithConfig 返回带自定义配置的 CORS 中间件。
func CorsWithConfig(config CorsConfig) Handler {
	normalized, configErr := normalizeCorsConfig(config)
	if configErr != nil {
		// CORS 是安全边界，配置错误时必须拒绝请求，不能退化为放行所有来源。
		return func(*context.Request, func(*context.Request) *context.Response) *context.Response {
			return context.NewResponse().Code(500).Content("invalid CORS configuration")
		}
	}
	config = normalized
	// 预计算，避免每个请求重复拼接
	allowMethodsStr := strings.Join(config.AllowMethods, ", ")
	allowHeadersStr := strings.Join(config.AllowHeaders, ", ")
	exposeHeadersStr := strings.Join(config.ExposeHeaders, ", ")
	maxAgeStr := ""
	if config.MaxAge > 0 {
		maxAgeStr = strconv.Itoa(config.MaxAge)
	}

	// 是否为通配符模式
	isWildcard := len(config.AllowOrigins) == 1 && config.AllowOrigins[0] == "*"

	return func(req *context.Request, next func(req *context.Request) *context.Response) *context.Response {
		origin := strings.TrimSpace(req.Header("Origin"))
		if origin == "" {
			if req.Method() == http.MethodOptions {
				return context.NewResponse().Code(http.StatusForbidden)
			}
			return next(req)
		}

		// 计算 Access-Control-Allow-Origin 与本次请求是否允许携带凭证。
		// 安全约束：通配符 "*" 永不与凭证组合（浏览器也会拒绝），
		// 且不会反射任意 Origin + 凭证——凭证仅对显式白名单命中的 Origin 生效，
		// 从而避免任意站点携带 Cookie 跨域读取响应。
		allowOrigin := ""
		allowCredentials := false
		if isWildcard {
			allowOrigin = "*"
			allowCredentials = false
		} else {
			for _, o := range config.AllowOrigins {
				if o == origin {
					allowOrigin = origin
					allowCredentials = config.AllowCredentials
					break
				}
			}
		}

		// Origin 不在白名单中，不设置 CORS 头
		if allowOrigin == "" {
			if req.Method() == http.MethodOptions {
				return context.NewResponse().Code(http.StatusForbidden)
			}
			return next(req)
		}

		// 处理预检请求（OPTIONS）
		if req.Method() == http.MethodOptions {
			if requestedMethod := strings.TrimSpace(req.Header("Access-Control-Request-Method")); requestedMethod != "" {
				if !corsListContains(config.AllowMethods, requestedMethod) || !corsRequestedHeadersAllowed(config.AllowHeaders, req.Header("Access-Control-Request-Headers")) {
					return context.NewResponse().Code(http.StatusForbidden)
				}
			}
			resp := context.NewResponse().Code(204)
			addCorsHeaders(resp, allowOrigin, allowMethodsStr, allowHeadersStr,
				exposeHeadersStr, maxAgeStr, allowCredentials)
			return resp
		}
		if len(config.AllowMethods) > 0 && !corsListContains(config.AllowMethods, req.Method()) {
			return context.NewResponse().Code(http.StatusForbidden)
		}

		// 处理正常请求
		resp := next(req)
		if resp == nil {
			// 下游返回 nil 时兜底为 204，避免后续设置响应头时空指针 panic。
			resp = context.NewResponse().Code(204)
		}
		addCorsHeaders(resp, allowOrigin, allowMethodsStr, allowHeadersStr,
			exposeHeadersStr, maxAgeStr, allowCredentials)
		return resp
	}
}

// addCorsHeaders 添加 CORS 响应头
func addCorsHeaders(resp *context.Response, origin, methods, headers, expose, maxAge string, credentials bool) {
	resp.Header("Access-Control-Allow-Origin", origin)
	// 反射具体 Origin 时声明 Vary，避免共享缓存把某来源的 CORS 响应错发给其他来源。
	if origin != "*" {
		addVaryValue(resp, "Origin")
	}
	resp.Header("Access-Control-Allow-Methods", methods)
	resp.Header("Access-Control-Allow-Headers", headers)

	if expose != "" {
		resp.Header("Access-Control-Expose-Headers", expose)
	}
	if maxAge != "" {
		resp.Header("Access-Control-Max-Age", maxAge)
	}
	if credentials {
		resp.Header("Access-Control-Allow-Credentials", "true")
	}
}

// addVaryValue 合并既有 Vary 值，避免 CORS 中间件覆盖压缩或语言协商策略。
func addVaryValue(resp *context.Response, value string) {
	if resp == nil || strings.TrimSpace(value) == "" {
		return
	}
	values := resp.Headers().Values("Vary")
	seen := make(map[string]struct{})
	merged := make([]string, 0, len(values)+1)
	for _, headerValue := range values {
		for _, token := range strings.Split(headerValue, ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			key := strings.ToLower(token)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, token)
		}
	}
	key := strings.ToLower(strings.TrimSpace(value))
	if _, exists := seen[key]; !exists {
		merged = append(merged, strings.TrimSpace(value))
	}
	resp.Header("Vary", strings.Join(merged, ", "))
}
