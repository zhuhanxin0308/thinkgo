package cookie

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CookieConfig Cookie 配置（启动时预解析，不可变）
type CookieConfig struct {
	Prefix   string // Cookie 名称前缀
	Secret   string // 签名密钥（为空则不签名）
	Path     string // 默认路径
	Domain   string // 默认域名
	Secure   bool   // 是否仅 HTTPS
	HttpOnly bool   // 是否 HttpOnly
	SameSite string // SameSite 策略
	Expire   int    // 默认过期时间（秒）
}

// ParseConfig 从 map 解析 Cookie 配置
func ParseConfig(m map[string]interface{}) CookieConfig {
	cfg := CookieConfig{
		Path:     "/",
		HttpOnly: true,
		SameSite: "Lax",
	}
	if v, ok := m["prefix"].(string); ok {
		cfg.Prefix = v
	}
	if v, ok := m["secret"].(string); ok {
		cfg.Secret = v
	}
	if v, ok := m["path"].(string); ok {
		cfg.Path = v
	}
	if v, ok := m["domain"].(string); ok {
		cfg.Domain = v
	}
	if v, ok := m["secure"].(bool); ok {
		cfg.Secure = v
	}
	if v, ok := m["httponly"].(bool); ok {
		cfg.HttpOnly = v
	}
	if v, ok := m["samesite"].(string); ok && strings.TrimSpace(v) != "" {
		cfg.SameSite = strings.TrimSpace(v)
	}
	if v, ok := m["expire"].(float64); ok {
		cfg.Expire = int(v)
	} else if v, ok := m["expire"].(int); ok {
		cfg.Expire = v
	}
	return cfg
}

// Cookie 管理器（每个请求创建独立实例，并发安全）
// 对应 ThinkPHP 8 的 think\Cookie
type Cookie struct {
	config  CookieConfig        // 全局配置（只读，线程安全）
	request *http.Request       // 当前请求
	writer  http.ResponseWriter // 当前响应
}

// NewCookie 创建 Cookie 管理器（旧 API 兼容）
func NewCookie(config map[string]interface{}) *Cookie {
	return &Cookie{
		config: ParseConfig(config),
	}
}

// NewCookieForRequest 为每个请求创建独立的 Cookie 实例（推荐）
// 不同请求拥有独立的 request/writer，不会并发冲突
func NewCookieForRequest(config CookieConfig, req *http.Request, w http.ResponseWriter) *Cookie {
	return &Cookie{
		config:  config,
		request: req,
		writer:  w,
	}
}

// Init 初始化请求和响应（旧 API 兼容，不推荐在并发环境使用）
func (c *Cookie) Init(req *http.Request, w http.ResponseWriter) {
	c.request = req
	c.writer = w
}

// SetWriter 设置响应 writer
func (c *Cookie) SetWriter(w http.ResponseWriter) {
	c.writer = w
}

// GetConfig 获取 Cookie 全局配置（只读）
// 供 Session 等模块创建请求级 Cookie 实例时复用配置
func (c *Cookie) GetConfig() CookieConfig {
	return c.config
}

// Set 设置 Cookie
func (c *Cookie) Set(name string, value string, options ...map[string]interface{}) {
	if c == nil || c.writer == nil {
		// 旧 API 可能在 Init/SetWriter 前被调用；方法无错误返回值，只能安全降级为无操作。
		return
	}

	opts := c.mergeOptions(options...)

	// 签名（绑定 Cookie 名称，避免签名值在不同 Cookie 间被调换）
	if c.config.Secret != "" {
		value = c.sign(c.config.Prefix+name, value, c.config.Secret)
	}

	secure := opts.Secure
	// 纵深防御：HTTPS 请求下强制 Secure（即便配置未开启），避免会话/凭证 Cookie 走明文回传；
	// 本地 HTTP 开发仍可正常工作。
	if isSecureCookieRequest(c.request) {
		secure = true
	}
	// 浏览器要求 SameSite=None 必须配合 Secure，否则 Cookie 会被丢弃。
	if strings.EqualFold(opts.SameSite, "none") {
		secure = true
	}

	cookie := &http.Cookie{
		Name:     c.config.Prefix + name,
		Value:    value,
		Path:     opts.Path,
		Domain:   opts.Domain,
		Secure:   secure,
		HttpOnly: opts.HttpOnly,
	}

	if opts.Expire > 0 {
		cookie.Expires = time.Now().Add(time.Duration(opts.Expire) * time.Second)
	} else if opts.Expire < 0 {
		cookie.Expires = time.Unix(0, 0) // 删除 Cookie
		cookie.MaxAge = -1
	}

	switch strings.ToLower(opts.SameSite) {
	case "lax":
		cookie.SameSite = http.SameSiteLaxMode
	case "strict":
		cookie.SameSite = http.SameSiteStrictMode
	case "none":
		cookie.SameSite = http.SameSiteNoneMode
	}

	http.SetCookie(c.writer, cookie)
}

// isSecureCookieRequest 判断当前请求是否处于安全链路，兼容本机 HTTPS 反向代理。
func isSecureCookieRequest(req *http.Request) bool {
	if req == nil {
		return false
	}
	if req.TLS != nil {
		return true
	}
	if !isLoopbackRemoteAddr(req.RemoteAddr) {
		return false
	}
	proto := strings.TrimSpace(strings.Split(req.Header.Get("X-Forwarded-Proto"), ",")[0])
	return strings.EqualFold(proto, "https")
}

// isLoopbackRemoteAddr 只允许本机代理声明 HTTPS，避免远程客户端伪造代理头。
func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = strings.Trim(remoteAddr, "[]")
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Get 获取 Cookie 值
func (c *Cookie) Get(name string) string {
	if c == nil || c.request == nil {
		return ""
	}

	cookie, err := c.request.Cookie(c.config.Prefix + name)
	if err != nil {
		return ""
	}

	value := cookie.Value
	if c.config.Secret != "" {
		val, valid := c.unsign(c.config.Prefix+name, value, c.config.Secret)
		if !valid {
			return ""
		}
		return val
	}

	return value
}

// Has 检查 Cookie 是否存在
func (c *Cookie) Has(name string) bool {
	if c == nil || c.request == nil {
		return false
	}
	cookie, err := c.request.Cookie(c.config.Prefix + name)
	if err != nil {
		return false
	}
	if c.config.Secret != "" {
		// 签名 Cookie 的“存在”必须以验签通过为准，避免篡改值绕过业务的 Has 判断。
		_, valid := c.unsign(c.config.Prefix+name, cookie.Value, c.config.Secret)
		return valid
	}
	return true
}

// Delete 删除 Cookie
func (c *Cookie) Delete(name string) {
	c.Set(name, "", map[string]interface{}{"expire": -1})
}

// cookieOptions Cookie 选项（内部使用）
type cookieOptions struct {
	Path     string
	Domain   string
	Secure   bool
	HttpOnly bool
	SameSite string
	Expire   int
}

// mergeOptions 合并配置和自定义选项
func (c *Cookie) mergeOptions(options ...map[string]interface{}) cookieOptions {
	opts := cookieOptions{
		Path:     c.config.Path,
		Domain:   c.config.Domain,
		Secure:   c.config.Secure,
		HttpOnly: c.config.HttpOnly,
		SameSite: c.config.SameSite,
		Expire:   c.config.Expire,
	}

	if len(options) > 0 {
		opt := options[0]
		if v, ok := opt["path"].(string); ok {
			opts.Path = v
		}
		if v, ok := opt["domain"].(string); ok {
			opts.Domain = v
		}
		if v, ok := opt["secure"].(bool); ok {
			opts.Secure = v
		}
		if v, ok := opt["httponly"].(bool); ok {
			opts.HttpOnly = v
		}
		if v, ok := opt["samesite"].(string); ok {
			opts.SameSite = v
		}
		if v, ok := opt["expire"].(int); ok {
			opts.Expire = v
		} else if v, ok := opt["expire"].(float64); ok {
			opts.Expire = int(v)
		}
	}
	return opts
}

// signMaxAge 签名值的最大有效期（默认 7 天），超过后验签失败。
const signMaxAge = 7 * 24 * 3600

// sign 签名值，格式: value|timestamp.signature
// 时间戳嵌入签名内容中，防止签名值被永久重放；
// HMAC 额外覆盖 Cookie 名称（name），防止签名值在不同 Cookie 间被调换。
func (c *Cookie) sign(name, value, secret string) string {
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	payload := value + "|" + timestamp
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(name + "\x00" + payload))
	signature := base64.URLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + signature
}

// unsign 验证签名并返回原始值。
// HMAC 覆盖 Cookie 名称与时间戳，名称不匹配或已过期都会验签失败。
func (c *Cookie) unsign(name, value, secret string) (string, bool) {
	lastDot := strings.LastIndex(value, ".")
	if lastDot < 0 {
		return "", false
	}

	payload := value[:lastDot]
	sig := value[lastDot+1:]

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(name + "\x00" + payload))
	expectedSig := base64.URLEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return "", false
	}

	// 解析时间戳并检查有效期。签名内容必须包含时间戳，
	// 否则视为非法格式直接拒绝，避免无时效检查的旧格式被永久重放。
	pipeIdx := strings.LastIndex(payload, "|")
	if pipeIdx < 0 {
		return "", false
	}

	originalValue := payload[:pipeIdx]
	timestampStr := payload[pipeIdx+1:]
	ts, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return "", false
	}
	if time.Now().Unix()-ts > signMaxAge {
		return "", false // 签名已过期
	}
	return originalValue, true
}
