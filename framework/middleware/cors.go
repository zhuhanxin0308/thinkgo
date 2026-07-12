package middleware

import (
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
func CorsWithConfig(config CorsConfig) Handler {
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
		origin := req.Header("Origin")

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
			if req.Method() == "OPTIONS" {
				return context.NewResponse().Code(403)
			}
			return next(req)
		}

		// 处理预检请求（OPTIONS）
		if req.Method() == "OPTIONS" {
			resp := context.NewResponse().Code(204)
			addCorsHeaders(resp, allowOrigin, allowMethodsStr, allowHeadersStr,
				exposeHeadersStr, maxAgeStr, allowCredentials)
			return resp
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
		resp.Header("Vary", "Origin")
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
