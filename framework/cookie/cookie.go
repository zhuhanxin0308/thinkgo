package cookie

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	defaultCookieSignMaxAge = 7 * 24 * 3600
	maxCookieAgeSeconds     = 400 * 24 * 3600
	maxCookieHeaderBytes    = 4096
	maxCookieClockSkew      = 5 * time.Minute
	minCookieSecretBytes    = 32
	maxCookieSecretBytes    = 4096
	maxCookieNameBytes      = 256
)

var (
	// ErrInvalidCookieConfig 表示 Cookie 配置字段、类型或范围非法。
	ErrInvalidCookieConfig = errors.New("Cookie 配置非法")
	// ErrInvalidCookieName 表示最终 Cookie 名称不符合 RFC 语法或超过长度限制。
	ErrInvalidCookieName = errors.New("Cookie 名称非法")
	// ErrInvalidCookieValue 表示未签名 Cookie 值不符合 HTTP Cookie 语法。
	ErrInvalidCookieValue = errors.New("Cookie 值非法")
	// ErrInvalidCookieOptions 表示单次写入选项非法。
	ErrInvalidCookieOptions = errors.New("Cookie 写入选项非法")
	// ErrCookieTooLarge 表示单个 Set-Cookie 字段超过安全上限。
	ErrCookieTooLarge = errors.New("Cookie 超过大小上限")
	// ErrCookieRequestUnavailable 表示读取操作没有绑定请求。
	ErrCookieRequestUnavailable = errors.New("Cookie 请求不可用")
	// ErrCookieWriterUnavailable 表示写入操作没有绑定响应 writer。
	ErrCookieWriterUnavailable = errors.New("Cookie 响应 writer 不可用")
	// ErrDuplicateCookie 表示请求携带多个同名 Cookie，存在解析歧义。
	ErrDuplicateCookie = errors.New("请求包含重复 Cookie")
	// ErrInvalidCookieSignature 表示签名格式、名称绑定、时间或 HMAC 校验失败。
	ErrInvalidCookieSignature = errors.New("Cookie 签名非法")
	// ErrExpiredCookieSignature 表示签名 Cookie 超出允许重放窗口。
	ErrExpiredCookieSignature = errors.New("Cookie 签名已过期")
)

// CookieConfig 是启动期严格解析后只读的 Cookie 全局配置。
type CookieConfig struct {
	Prefix     string
	Secret     string
	Path       string
	Domain     string
	Secure     bool
	HttpOnly   bool
	SameSite   string
	Expire     int
	SignMaxAge int
}

// CookieOptions 是单次写入可覆盖的 Cookie 属性；零值字段沿用全局配置。
type CookieOptions struct {
	Path     string
	Domain   string
	Secure   bool
	HttpOnly bool
	SameSite string
	Expire   int
}

// Cookie 是不可共享请求状态的 Cookie 工厂或请求级实例。
type Cookie struct {
	config           CookieConfig
	request          *http.Request
	writer           http.ResponseWriter
	requestSecure    bool
	requestSecureSet bool
	mu               sync.RWMutex
	writeMu          sync.Mutex
}

// DefaultConfig 返回安全的 Cookie 默认配置。
func DefaultConfig() CookieConfig {
	return CookieConfig{
		Path:       "/",
		HttpOnly:   true,
		SameSite:   "Lax",
		SignMaxAge: defaultCookieSignMaxAge,
	}
}

// ParseConfig 严格解析 Cookie 配置，拒绝未知字段、截断和无效安全属性。
func ParseConfig(raw map[string]interface{}) (CookieConfig, error) {
	allowed := map[string]bool{
		"prefix": true, "secret": true, "path": true, "domain": true,
		"secure": true, "httponly": true, "samesite": true,
		"expire": true, "sign_max_age": true,
	}
	for key := range raw {
		if !allowed[key] {
			return CookieConfig{}, invalidCookieConfig(key, "未知配置项")
		}
	}
	config := DefaultConfig()
	var err error
	if value, exists := raw["prefix"]; exists {
		config.Prefix, err = cookieConfigString(value, "prefix")
		if err != nil {
			return CookieConfig{}, err
		}
	}
	if value, exists := raw["secret"]; exists {
		config.Secret, err = cookieConfigString(value, "secret")
		if err != nil {
			return CookieConfig{}, err
		}
	}
	if value, exists := raw["path"]; exists {
		config.Path, err = cookieConfigString(value, "path")
		if err != nil {
			return CookieConfig{}, err
		}
	}
	if value, exists := raw["domain"]; exists {
		config.Domain, err = cookieConfigString(value, "domain")
		if err != nil {
			return CookieConfig{}, err
		}
	}
	if value, exists := raw["secure"]; exists {
		config.Secure, err = cookieConfigBool(value, "secure")
		if err != nil {
			return CookieConfig{}, err
		}
	}
	if value, exists := raw["httponly"]; exists {
		config.HttpOnly, err = cookieConfigBool(value, "httponly")
		if err != nil {
			return CookieConfig{}, err
		}
	}
	if value, exists := raw["samesite"]; exists {
		rawSameSite, stringErr := cookieConfigString(value, "samesite")
		if stringErr != nil {
			return CookieConfig{}, stringErr
		}
		config.SameSite, err = normalizeSameSite(rawSameSite)
		if err != nil {
			return CookieConfig{}, invalidCookieConfig("samesite", err.Error())
		}
	}
	if value, exists := raw["expire"]; exists {
		config.Expire, err = cookieConfigInteger(value, "expire", 0, maxCookieAgeSeconds)
		if err != nil {
			return CookieConfig{}, err
		}
	}
	if value, exists := raw["sign_max_age"]; exists {
		config.SignMaxAge, err = cookieConfigInteger(value, "sign_max_age", 1, maxCookieAgeSeconds)
		if err != nil {
			return CookieConfig{}, err
		}
	}
	if err = validateCookieConfig(config); err != nil {
		return CookieConfig{}, err
	}
	return config, nil
}

// NewCookie 创建只保存不可变配置的 Cookie 工厂。
func NewCookie(rawConfig map[string]interface{}) (*Cookie, error) {
	config, err := ParseConfig(rawConfig)
	if err != nil {
		return nil, err
	}
	return &Cookie{config: config}, nil
}

// NewCookieWithConfig 使用已解析配置创建 Cookie 工厂。
func NewCookieWithConfig(config CookieConfig) (*Cookie, error) {
	if err := validateCookieConfig(config); err != nil {
		return nil, err
	}
	return &Cookie{config: config}, nil
}

// NewCookieForRequest 创建请求级 Cookie；writer 可稍后通过 SetWriter 绑定。
func NewCookieForRequest(config CookieConfig, req *http.Request, writer http.ResponseWriter) (*Cookie, error) {
	if req == nil {
		return nil, ErrCookieRequestUnavailable
	}
	if err := validateCookieConfig(config); err != nil {
		return nil, err
	}
	return &Cookie{config: config, request: req, writer: writer}, nil
}

// NewCookieForRequestWithSecure 创建请求级 Cookie，并使用调用方已经完成可信代理校验的协议结论。
func NewCookieForRequestWithSecure(config CookieConfig, req *http.Request, writer http.ResponseWriter, secure bool) (*Cookie, error) {
	if req == nil {
		return nil, ErrCookieRequestUnavailable
	}
	if err := validateCookieConfig(config); err != nil {
		return nil, err
	}
	return &Cookie{config: config, request: req, writer: writer, requestSecure: secure, requestSecureSet: true}, nil
}

// ForRequest 从工厂创建隔离的请求级 Cookie 实例。
func (c *Cookie) ForRequest(req *http.Request, writer http.ResponseWriter) (*Cookie, error) {
	if c == nil {
		return nil, ErrInvalidCookieConfig
	}
	return NewCookieForRequest(c.config, req, writer)
}

// ForRequestWithSecure 从工厂创建请求级 Cookie，并显式传入可信协议结论。
func (c *Cookie) ForRequestWithSecure(req *http.Request, writer http.ResponseWriter, secure bool) (*Cookie, error) {
	if c == nil {
		return nil, ErrInvalidCookieConfig
	}
	return NewCookieForRequestWithSecure(c.config, req, writer, secure)
}

// SetWriter 为请求级 Cookie 绑定响应 writer。
func (c *Cookie) SetWriter(writer http.ResponseWriter) error {
	if c == nil || isNilResponseWriter(writer) {
		return ErrCookieWriterUnavailable
	}
	c.mu.Lock()
	c.writer = writer
	c.mu.Unlock()
	return nil
}

// GetConfig 返回不可变配置的值拷贝。
func (c *Cookie) GetConfig() CookieConfig {
	if c == nil {
		return CookieConfig{}
	}
	return c.config
}

// Set 写入 Cookie，并显式返回配置、签名和响应错误。
func (c *Cookie) Set(name, value string, options ...CookieOptions) error {
	if c == nil {
		return ErrCookieWriterUnavailable
	}
	header, err := c.BuildHeader(name, value, options...)
	if err != nil {
		return err
	}
	c.mu.RLock()
	writer := c.writer
	c.mu.RUnlock()
	if isNilResponseWriter(writer) {
		return ErrCookieWriterUnavailable
	}
	// http.Header 本身不保证并发安全，请求级 Cookie 的并发写入在此串行化。
	c.writeMu.Lock()
	writer.Header().Add("Set-Cookie", header)
	c.writeMu.Unlock()
	return nil
}

// isNilResponseWriter 同时识别 nil 接口和承载 nil 指针的接口，避免写 Cookie 时触发反射后的 panic。
func isNilResponseWriter(writer http.ResponseWriter) bool {
	if writer == nil {
		return true
	}
	value := reflect.ValueOf(writer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// BuildHeader 构建经过完整配置、签名和长度校验的 Set-Cookie 头值，不要求绑定 writer。
func (c *Cookie) BuildHeader(name, value string, options ...CookieOptions) (string, error) {
	if c == nil {
		return "", ErrInvalidCookieConfig
	}
	if len(options) > 1 {
		return "", ErrInvalidCookieOptions
	}
	finalName, err := c.finalName(name)
	if err != nil {
		return "", err
	}
	opts := c.defaultOptions()
	if len(options) == 1 {
		opts = mergeCookieOptions(opts, options[0])
	}
	if err = validateCookieOptions(opts); err != nil {
		return "", err
	}

	c.mu.RLock()
	request := c.request
	requestSecure := c.requestSecure
	requestSecureSet := c.requestSecureSet
	c.mu.RUnlock()
	now := time.Now()
	encodedValue := value
	if c.config.Secret != "" {
		encodedValue, err = signCookieValue(finalName, value, c.config.Secret, now)
		if err != nil {
			return "", err
		}
	}
	secure := opts.Secure || strings.EqualFold(opts.SameSite, "None")
	if requestSecureSet {
		secure = secure || requestSecure
	} else {
		secure = secure || isSecureCookieRequest(request)
	}
	// #nosec G124 -- 安全属性来自调用方已校验的 CookieOptions，保留显式兼容策略。
	cookie := &http.Cookie{
		Name:     finalName,
		Value:    encodedValue,
		Path:     opts.Path,
		Domain:   opts.Domain,
		Secure:   secure,
		HttpOnly: opts.HttpOnly,
		SameSite: sameSiteMode(opts.SameSite),
	}
	if opts.Expire > 0 {
		cookie.MaxAge = opts.Expire
		cookie.Expires = now.Add(time.Duration(opts.Expire) * time.Second)
	} else if opts.Expire < 0 {
		cookie.MaxAge = -1
		cookie.Expires = time.Unix(1, 0).UTC()
	}
	if err = cookie.Valid(); err != nil {
		if strings.Contains(err.Error(), "Cookie.Value") {
			return "", fmt.Errorf("%w: %v", ErrInvalidCookieValue, err)
		}
		return "", fmt.Errorf("%w: %v", ErrInvalidCookieOptions, err)
	}
	header := cookie.String()
	if header == "" {
		return "", ErrInvalidCookieValue
	}
	if len(header) > maxCookieHeaderBytes {
		return "", fmt.Errorf("%w: %d", ErrCookieTooLarge, len(header))
	}
	return header, nil
}

// Get 返回值、存在标志和验签错误，正确区分空值与缺失。
func (c *Cookie) Get(name string) (string, bool, error) {
	if c == nil {
		return "", false, ErrCookieRequestUnavailable
	}
	finalName, err := c.finalName(name)
	if err != nil {
		return "", false, err
	}
	c.mu.RLock()
	request := c.request
	c.mu.RUnlock()
	if request == nil {
		return "", false, ErrCookieRequestUnavailable
	}
	var matched *http.Cookie
	for _, item := range request.Cookies() {
		if item.Name != finalName {
			continue
		}
		if matched != nil {
			return "", false, fmt.Errorf("%w: %s", ErrDuplicateCookie, finalName)
		}
		copyItem := *item
		matched = &copyItem
	}
	if matched == nil {
		return "", false, nil
	}
	if len(matched.Value) > maxCookieHeaderBytes {
		return "", false, ErrCookieTooLarge
	}
	if c.config.Secret == "" {
		return matched.Value, true, nil
	}
	value, err := unsignCookieValue(finalName, matched.Value, c.config.Secret, c.effectiveSignMaxAge(), time.Now())
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// Has 判断 Cookie 是否存在且签名有效。
func (c *Cookie) Has(name string) (bool, error) {
	_, found, err := c.Get(name)
	return found, err
}

// Delete 使用相同 Path、Domain 和安全属性删除 Cookie。
func (c *Cookie) Delete(name string) error {
	opts := c.defaultOptions()
	opts.Expire = -1
	return c.Set(name, "", opts)
}

func (c *Cookie) finalName(name string) (string, error) {
	if c == nil || name == "" {
		return "", ErrInvalidCookieName
	}
	finalName := c.config.Prefix + name
	if len(finalName) > maxCookieNameBytes || hasCookieControl(finalName) {
		return "", fmt.Errorf("%w: %q", ErrInvalidCookieName, finalName)
	}
	// #nosec G124 -- 该 Cookie 只用于校验名称语法，不会写入响应。
	probe := &http.Cookie{Name: finalName, Value: "x", Path: "/"}
	if err := probe.Valid(); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCookieName, err)
	}
	return finalName, nil
}

func (c *Cookie) defaultOptions() CookieOptions {
	return CookieOptions{
		Path: c.config.Path, Domain: c.config.Domain, Secure: c.config.Secure,
		HttpOnly: c.config.HttpOnly, SameSite: c.config.SameSite, Expire: c.config.Expire,
	}
}

func (c *Cookie) effectiveSignMaxAge() int {
	maxAge := c.config.SignMaxAge
	if c.config.Expire > 0 && c.config.Expire < maxAge {
		maxAge = c.config.Expire
	}
	return maxAge
}

func mergeCookieOptions(base, override CookieOptions) CookieOptions {
	if override.Path != "" {
		base.Path = override.Path
	}
	if override.Domain != "" {
		base.Domain = override.Domain
	}
	base.Secure = base.Secure || override.Secure
	base.HttpOnly = base.HttpOnly || override.HttpOnly
	if override.SameSite != "" {
		base.SameSite = override.SameSite
	}
	if override.Expire != 0 {
		base.Expire = override.Expire
	}
	return base
}

func validateCookieConfig(config CookieConfig) error {
	if !utf8.ValidString(config.Prefix) || len(config.Prefix) > maxCookieNameBytes || hasCookieControl(config.Prefix) {
		return invalidCookieConfig("prefix", "包含非法字符或过长")
	}
	if config.Prefix != "" {
		// #nosec G124 -- 该 Cookie 只用于校验前缀语法，不会写入响应。
		probe := &http.Cookie{Name: config.Prefix + "x", Value: "x", Path: "/"}
		if err := probe.Valid(); err != nil {
			return invalidCookieConfig("prefix", err.Error())
		}
	}
	if config.Secret != "" && (len(config.Secret) < minCookieSecretBytes || len(config.Secret) > maxCookieSecretBytes) {
		return invalidCookieConfig("secret", "非空密钥必须为 32 至 4096 字节")
	}
	if config.SignMaxAge < 1 || config.SignMaxAge > maxCookieAgeSeconds {
		return invalidCookieConfig("sign_max_age", "超出允许范围")
	}
	if config.Expire < 0 || config.Expire > maxCookieAgeSeconds {
		return invalidCookieConfig("expire", "超出允许范围")
	}
	if _, err := normalizeSameSite(config.SameSite); err != nil {
		return invalidCookieConfig("samesite", err.Error())
	}
	if err := validateCookiePolicy(config.Path, config.Domain); err != nil {
		return invalidCookieConfig("path/domain", err.Error())
	}
	return nil
}

func validateCookieOptions(options CookieOptions) error {
	if options.Expire < -1 || options.Expire > maxCookieAgeSeconds {
		return fmt.Errorf("%w: expire", ErrInvalidCookieOptions)
	}
	if _, err := normalizeSameSite(options.SameSite); err != nil {
		return fmt.Errorf("%w: samesite: %v", ErrInvalidCookieOptions, err)
	}
	if err := validateCookiePolicy(options.Path, options.Domain); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCookieOptions, err)
	}
	return nil
}

func validateCookiePolicy(path, domain string) error {
	if path == "" || !strings.HasPrefix(path, "/") || hasCookieControl(path) || strings.Contains(path, ";") {
		return errors.New("Cookie Path 必须以 / 开头且不含控制字符或分号")
	}
	// #nosec G124 -- 该 Cookie 只用于校验路径和域名语法，不会写入响应。
	probe := &http.Cookie{Name: "probe", Value: "x", Path: path, Domain: domain}
	if err := probe.Valid(); err != nil {
		return err
	}
	return nil
}

func normalizeSameSite(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "lax":
		return "Lax", nil
	case "strict":
		return "Strict", nil
	case "none":
		return "None", nil
	default:
		return "", errors.New("SameSite 仅支持 Lax、Strict、None")
	}
}

func sameSiteMode(value string) http.SameSite {
	switch strings.ToLower(value) {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}

func signCookieValue(name, value, secret string, now time.Time) (string, error) {
	if name == "" || len(secret) < minCookieSecretBytes || !utf8.ValidString(value) {
		return "", ErrInvalidCookieSignature
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(value))
	timestamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(name + "\x00v1\x00" + payload + "\x00" + timestamp))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return "v1." + payload + "." + timestamp + "." + signature, nil
}

func unsignCookieValue(name, signed, secret string, maxAge int, now time.Time) (string, error) {
	parts := strings.Split(signed, ".")
	if len(parts) != 4 || parts[0] != "v1" || name == "" || len(secret) < minCookieSecretBytes || maxAge <= 0 {
		return "", ErrInvalidCookieSignature
	}
	timestamp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", ErrInvalidCookieSignature
	}
	signedAt := time.Unix(timestamp, 0)
	if signedAt.After(now.Add(maxCookieClockSkew)) {
		return "", ErrInvalidCookieSignature
	}
	if now.Sub(signedAt) > time.Duration(maxAge)*time.Second {
		return "", ErrExpiredCookieSignature
	}
	providedSignature, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(providedSignature) != sha256.Size {
		return "", ErrInvalidCookieSignature
	}
	if base64.RawURLEncoding.EncodeToString(providedSignature) != parts[3] {
		return "", ErrInvalidCookieSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(name + "\x00v1\x00" + parts[1] + "\x00" + parts[2]))
	if !hmac.Equal(providedSignature, mac.Sum(nil)) {
		return "", ErrInvalidCookieSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !utf8.Valid(payload) {
		return "", ErrInvalidCookieSignature
	}
	if base64.RawURLEncoding.EncodeToString(payload) != parts[1] {
		return "", ErrInvalidCookieSignature
	}
	return string(payload), nil
}

func cookieConfigString(value interface{}, field string) (string, error) {
	text, ok := value.(string)
	if !ok || !utf8.ValidString(text) || hasCookieControl(text) {
		return "", invalidCookieConfig(field, "必须是不含控制字符的字符串")
	}
	return text, nil
}

func cookieConfigBool(value interface{}, field string) (bool, error) {
	parsed, ok := value.(bool)
	if !ok {
		return false, invalidCookieConfig(field, "必须是布尔值")
	}
	return parsed, nil
}

func cookieConfigInteger(value interface{}, field string, minimum, maximum int) (int, error) {
	parsed, ok := exactCookieInteger(value)
	if !ok || parsed < int64(minimum) || parsed > int64(maximum) {
		return 0, invalidCookieConfig(field, fmt.Sprintf("必须是 %d 至 %d 的整数", minimum, maximum))
	}
	return int(parsed), nil
}

func exactCookieInteger(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case float32:
		return exactCookieFloat(float64(typed))
	case float64:
		return exactCookieFloat(typed)
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func exactCookieFloat(value float64) (int64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
		value < float64(math.MinInt64) || value >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(value), true
}

func invalidCookieConfig(field, reason string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidCookieConfig, field, reason)
}

func hasCookieControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

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

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = strings.Trim(remoteAddr, "[]")
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
