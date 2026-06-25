package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	"thinkgo/framework/context"
)

// CSRF 防护中间件（双重提交 Cookie 模式 / Double Submit Cookie）。
//
// 工作原理：
//   - 对安全方法（GET/HEAD/OPTIONS）放行，并在响应中下发一个随机 CSRF token Cookie（非 HttpOnly，
//     便于前端 JS 读取后回填到请求头）。
//   - 对状态变更方法（POST/PUT/PATCH/DELETE 等）要求请求同时携带 Cookie 中的 token 与
//     请求头（默认 X-CSRF-Token）或表单字段（默认 _csrf）中的 token，且二者常量时间比较一致，
//     否则返回 403。
//
// 该模式无需服务端会话存储，天然并发安全，适合前后端分离与传统表单两种场景。
const (
	DefaultCSRFCookieName = "csrf_token"
	DefaultCSRFHeaderName = "X-CSRF-Token"
	DefaultCSRFFieldName  = "_csrf"
	csrfTokenBytes        = 32
)

// CSRFConfig CSRF 中间件配置。
type CSRFConfig struct {
	CookieName   string
	HeaderName   string
	FieldName    string
	CookiePath   string
	CookieDomain string
	Secure       bool
	SameSite     string
	MaxAge       int
	// SafeMethods 不需要校验 token 的方法集合（默认 GET/HEAD/OPTIONS）。
	SafeMethods map[string]bool
}

// DefaultCSRFConfig 返回默认 CSRF 配置。
func DefaultCSRFConfig() CSRFConfig {
	return CSRFConfig{
		CookieName: DefaultCSRFCookieName,
		HeaderName: DefaultCSRFHeaderName,
		FieldName:  DefaultCSRFFieldName,
		CookiePath: "/",
		Secure:     false,
		SameSite:   "Lax",
		MaxAge:     7 * 24 * 3600,
		SafeMethods: map[string]bool{
			http.MethodGet:     true,
			http.MethodHead:    true,
			http.MethodOptions: true,
			http.MethodTrace:   true,
		},
	}
}

// Csrf 返回使用默认配置的 CSRF 中间件。
func Csrf() Handler {
	return CsrfWithConfig(DefaultCSRFConfig())
}

// CsrfWithConfig 返回带自定义配置的 CSRF 中间件。
func CsrfWithConfig(config CSRFConfig) Handler {
	if config.CookieName == "" {
		config.CookieName = DefaultCSRFCookieName
	}
	if config.HeaderName == "" {
		config.HeaderName = DefaultCSRFHeaderName
	}
	if config.FieldName == "" {
		config.FieldName = DefaultCSRFFieldName
	}
	if config.CookiePath == "" {
		config.CookiePath = "/"
	}
	if len(config.SafeMethods) == 0 {
		config.SafeMethods = DefaultCSRFConfig().SafeMethods
	}

	return func(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
		method := strings.ToUpper(req.Method())
		cookieToken := req.Cookie(config.CookieName)

		if config.SafeMethods[method] {
			resp := next(req)
			if resp == nil {
				resp = context.NewResponse()
			}
			// 首次访问尚无 token 时下发一个新 token，供后续状态变更请求回填。
			if cookieToken == "" {
				if token, err := generateCSRFToken(); err == nil {
					setCSRFCookie(resp, config, token)
				}
			}
			return resp
		}

		// 状态变更请求：校验双重提交 token。
		submitted := req.Header(config.HeaderName)
		if submitted == "" {
			submitted = req.Post(config.FieldName)
		}

		if cookieToken == "" || submitted == "" ||
			subtle.ConstantTimeCompare([]byte(cookieToken), []byte(submitted)) != 1 {
			return context.NewResponse().Code(http.StatusForbidden).Json(map[string]interface{}{
				"code": http.StatusForbidden,
				"msg":  "CSRF token mismatch",
				"data": nil,
			})
		}

		return next(req)
	}
}

// generateCSRFToken 生成密码学安全的随机 token。
func generateCSRFToken() (string, error) {
	buf := make([]byte, csrfTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// setCSRFCookie 把 token 写入响应 Cookie。
// 该 Cookie 刻意不设 HttpOnly，以便前端读取并回填到请求头（双重提交模式所需）。
func setCSRFCookie(resp *context.Response, config CSRFConfig, token string) {
	cookie := &http.Cookie{
		Name:     config.CookieName,
		Value:    token,
		Path:     config.CookiePath,
		Domain:   config.CookieDomain,
		MaxAge:   config.MaxAge,
		Secure:   config.Secure,
		HttpOnly: false,
	}
	switch strings.ToLower(config.SameSite) {
	case "strict":
		cookie.SameSite = http.SameSiteStrictMode
	case "none":
		cookie.SameSite = http.SameSiteNoneMode
		cookie.Secure = true // 浏览器要求 SameSite=None 必须配合 Secure。
	default:
		cookie.SameSite = http.SameSiteLaxMode
	}
	resp.Headers().Add("Set-Cookie", cookie.String())
}
