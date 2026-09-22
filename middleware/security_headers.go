package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
)

const (
	maximumSecurityPolicyBytes = 16 * 1024
	minimumHSTSPreloadSeconds  = 365 * 24 * 60 * 60
)

var (
	// ErrInvalidSecurityHeaders 表示安全响应头策略为空、包含注入字符或组合不安全。
	ErrInvalidSecurityHeaders = errors.New("安全响应头配置非法")
)

// SecurityHeadersConfig 描述统一应用于框架响应和 net/http 处理器的浏览器安全策略。
type SecurityHeadersConfig struct {
	ContentSecurityPolicy     string
	ReferrerPolicy            string
	PermissionsPolicy         string
	CrossOriginOpenerPolicy   string
	CrossOriginResourcePolicy string
	FrameOptions              string
	HSTSMaxAgeSeconds         int
	HSTSIncludeSubDomains     bool
	HSTSPreload               bool
}

type securityHeaders struct {
	headers http.Header
	hsts    string
}

// DefaultSecurityHeadersConfig 返回保守、不会允许内联脚本的浏览器安全默认值。
func DefaultSecurityHeadersConfig() SecurityHeadersConfig {
	return SecurityHeadersConfig{
		ContentSecurityPolicy:     "default-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'",
		ReferrerPolicy:            "strict-origin-when-cross-origin",
		PermissionsPolicy:         "camera=(), microphone=(), geolocation=()",
		CrossOriginOpenerPolicy:   "same-origin",
		CrossOriginResourcePolicy: "same-origin",
		FrameOptions:              "DENY",
	}
}

// NewSecurityHeaders 校验并冻结安全策略，返回可放入全局管道的中间件。
func NewSecurityHeaders(config SecurityHeadersConfig) (Handler, error) {
	config = mergeSecurityHeaderDefaults(config)
	if err := validateSecurityHeaders(config); err != nil {
		return nil, err
	}
	policy := &securityHeaders{headers: make(http.Header)}
	policy.headers.Set("X-Content-Type-Options", "nosniff")
	policy.headers.Set("X-Frame-Options", config.FrameOptions)
	policy.headers.Set("Content-Security-Policy", config.ContentSecurityPolicy)
	policy.headers.Set("Referrer-Policy", config.ReferrerPolicy)
	policy.headers.Set("Permissions-Policy", config.PermissionsPolicy)
	policy.headers.Set("Cross-Origin-Opener-Policy", config.CrossOriginOpenerPolicy)
	policy.headers.Set("Cross-Origin-Resource-Policy", config.CrossOriginResourcePolicy)
	if config.HSTSMaxAgeSeconds > 0 {
		policy.hsts = "max-age=" + strconv.Itoa(config.HSTSMaxAgeSeconds)
		if config.HSTSIncludeSubDomains {
			policy.hsts += "; includeSubDomains"
		}
		if config.HSTSPreload {
			policy.hsts += "; preload"
		}
	}
	return policy.Handle, nil
}

func mergeSecurityHeaderDefaults(config SecurityHeadersConfig) SecurityHeadersConfig {
	defaults := DefaultSecurityHeadersConfig()
	if strings.TrimSpace(config.ContentSecurityPolicy) == "" {
		config.ContentSecurityPolicy = defaults.ContentSecurityPolicy
	}
	if strings.TrimSpace(config.ReferrerPolicy) == "" {
		config.ReferrerPolicy = defaults.ReferrerPolicy
	}
	if strings.TrimSpace(config.PermissionsPolicy) == "" {
		config.PermissionsPolicy = defaults.PermissionsPolicy
	}
	if strings.TrimSpace(config.CrossOriginOpenerPolicy) == "" {
		config.CrossOriginOpenerPolicy = defaults.CrossOriginOpenerPolicy
	}
	if strings.TrimSpace(config.CrossOriginResourcePolicy) == "" {
		config.CrossOriginResourcePolicy = defaults.CrossOriginResourcePolicy
	}
	if strings.TrimSpace(config.FrameOptions) == "" {
		config.FrameOptions = defaults.FrameOptions
	}
	return config
}

func validateSecurityHeaders(config SecurityHeadersConfig) error {
	values := map[string]string{
		"Content-Security-Policy":      config.ContentSecurityPolicy,
		"Referrer-Policy":              config.ReferrerPolicy,
		"Permissions-Policy":           config.PermissionsPolicy,
		"Cross-Origin-Opener-Policy":   config.CrossOriginOpenerPolicy,
		"Cross-Origin-Resource-Policy": config.CrossOriginResourcePolicy,
		"X-Frame-Options":              config.FrameOptions,
	}
	for name, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > maximumSecurityPolicyBytes || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%w: %s", ErrInvalidSecurityHeaders, name)
		}
	}
	if !allowedSecurityValue(strings.ToLower(config.ReferrerPolicy), "no-referrer", "no-referrer-when-downgrade", "origin", "origin-when-cross-origin", "same-origin", "strict-origin", "strict-origin-when-cross-origin") {
		return fmt.Errorf("%w: Referrer-Policy", ErrInvalidSecurityHeaders)
	}
	if !allowedSecurityValue(strings.ToLower(config.CrossOriginOpenerPolicy), "unsafe-none", "same-origin-allow-popups", "same-origin") {
		return fmt.Errorf("%w: Cross-Origin-Opener-Policy", ErrInvalidSecurityHeaders)
	}
	if !allowedSecurityValue(strings.ToLower(config.CrossOriginResourcePolicy), "same-site", "same-origin", "cross-origin") {
		return fmt.Errorf("%w: Cross-Origin-Resource-Policy", ErrInvalidSecurityHeaders)
	}
	if !allowedSecurityValue(strings.ToUpper(config.FrameOptions), "DENY", "SAMEORIGIN") {
		return fmt.Errorf("%w: X-Frame-Options", ErrInvalidSecurityHeaders)
	}
	if config.HSTSMaxAgeSeconds < 0 {
		return fmt.Errorf("%w: HSTS max-age", ErrInvalidSecurityHeaders)
	}
	if config.HSTSPreload && (config.HSTSMaxAgeSeconds < minimumHSTSPreloadSeconds || !config.HSTSIncludeSubDomains) {
		return fmt.Errorf("%w: HSTS preload 需要至少一年 max-age 和 includeSubDomains", ErrInvalidSecurityHeaders)
	}
	return nil
}

func allowedSecurityValue(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

// Handle 在下游写出前设置 Writer，并在框架响应上再次覆盖安全策略。
func (policy *securityHeaders) Handle(request *context.Request, next func(*context.Request) *context.Response) *context.Response {
	if request == nil || next == nil {
		return context.NewResponse().Code(http.StatusInternalServerError).Content(http.StatusText(http.StatusInternalServerError))
	}
	headers := policy.headers.Clone()
	if policy.hsts != "" && request.IsSsl() {
		headers.Set("Strict-Transport-Security", policy.hsts)
	}
	if writer, exists := request.ResponseWriter(); exists {
		for name, values := range headers {
			writer.Header()[name] = append([]string(nil), values...)
		}
	}
	response := next(request)
	if response == nil {
		response = context.NewResponse().Code(http.StatusInternalServerError).Content(http.StatusText(http.StatusInternalServerError))
	}
	for name, values := range headers {
		if len(values) > 0 {
			response.Header(name, values[0])
		}
	}
	return response
}
