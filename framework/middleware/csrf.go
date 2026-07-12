package middleware

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"thinkgo/framework/context"
)

const (
	DefaultCSRFCookieName = "csrf_token"
	DefaultCSRFHeaderName = "X-CSRF-Token"
	DefaultCSRFFieldName  = "_csrf"
	csrfTokenBytes        = 32
	minCSRFSecretBytes    = 32
	maxCSRFSecretBytes    = 4096
	maxCSRFTokenBytes     = 512
	maxCSRFCookieBytes    = 4096
	maxCSRFAgeSeconds     = 400 * 24 * 3600
	maxCSRFClockSkew      = 5 * time.Minute
)

var (
	// ErrInvalidCSRFConfig 表示 CSRF 配置字段、类型或安全策略非法。
	ErrInvalidCSRFConfig = errors.New("CSRF 配置非法")
	// ErrInvalidCSRFToken 表示 token 格式、随机数或 HMAC 校验失败。
	ErrInvalidCSRFToken = errors.New("CSRF token 非法")
	// ErrExpiredCSRFToken 表示 token 已超过允许重放窗口。
	ErrExpiredCSRFToken = errors.New("CSRF token 已过期")
	// ErrDuplicateCSRFCookie 表示请求包含多个同名 CSRF Cookie。
	ErrDuplicateCSRFCookie = errors.New("请求包含重复 CSRF Cookie")
	// ErrCSRFTokenGeneration 表示密码学随机源不可用。
	ErrCSRFTokenGeneration = errors.New("生成 CSRF token 失败")
)

// CSRFConfig 是启动期校验后按值复制的签名双重提交配置。
type CSRFConfig struct {
	CookieName   string
	HeaderName   string
	FieldName    string
	CookiePath   string
	CookieDomain string
	Secure       bool
	SameSite     string
	MaxAge       int
	Secret       string
	SafeMethods  []string
}

type csrfMiddleware struct {
	config      CSRFConfig
	safeMethods map[string]struct{}
	random      io.Reader
	now         func() time.Time
}

// DefaultCSRFConfig 返回默认安全方法与 Cookie 策略。
func DefaultCSRFConfig() CSRFConfig {
	return CSRFConfig{
		CookieName: DefaultCSRFCookieName,
		HeaderName: DefaultCSRFHeaderName,
		FieldName:  DefaultCSRFFieldName,
		CookiePath: "/",
		SameSite:   "Lax",
		MaxAge:     7 * 24 * 3600,
		SafeMethods: []string{
			http.MethodGet,
			http.MethodHead,
			http.MethodOptions,
		},
	}
}

// ParseCSRFConfig 严格解析配置，拒绝未知字段、危险方法和数值截断。
func ParseCSRFConfig(raw map[string]interface{}) (CSRFConfig, error) {
	allowed := map[string]bool{
		"cookie_name": true, "header_name": true, "field_name": true,
		"cookie_path": true, "cookie_domain": true, "secure": true,
		"samesite": true, "max_age": true, "secret": true, "safe_methods": true,
	}
	for key := range raw {
		if !allowed[key] {
			return CSRFConfig{}, invalidCSRFConfig(key, "未知配置项")
		}
	}
	config := DefaultCSRFConfig()
	var err error
	if value, exists := raw["cookie_name"]; exists {
		config.CookieName, err = csrfConfigString(value, "cookie_name")
		if err != nil {
			return CSRFConfig{}, err
		}
	}
	if value, exists := raw["header_name"]; exists {
		config.HeaderName, err = csrfConfigString(value, "header_name")
		if err != nil {
			return CSRFConfig{}, err
		}
	}
	if value, exists := raw["field_name"]; exists {
		config.FieldName, err = csrfConfigString(value, "field_name")
		if err != nil {
			return CSRFConfig{}, err
		}
	}
	if value, exists := raw["cookie_path"]; exists {
		config.CookiePath, err = csrfConfigString(value, "cookie_path")
		if err != nil {
			return CSRFConfig{}, err
		}
	}
	if value, exists := raw["cookie_domain"]; exists {
		config.CookieDomain, err = csrfConfigString(value, "cookie_domain")
		if err != nil {
			return CSRFConfig{}, err
		}
	}
	if value, exists := raw["secure"]; exists {
		config.Secure, err = csrfConfigBool(value, "secure")
		if err != nil {
			return CSRFConfig{}, err
		}
	}
	if value, exists := raw["samesite"]; exists {
		var rawSameSite string
		rawSameSite, err = csrfConfigString(value, "samesite")
		if err != nil {
			return CSRFConfig{}, err
		}
		config.SameSite, err = normalizeCSRFSameSite(rawSameSite)
		if err != nil {
			return CSRFConfig{}, invalidCSRFConfig("samesite", err.Error())
		}
	}
	if value, exists := raw["max_age"]; exists {
		config.MaxAge, err = csrfConfigInteger(value, "max_age", 1, maxCSRFAgeSeconds)
		if err != nil {
			return CSRFConfig{}, err
		}
	}
	if value, exists := raw["secret"]; exists {
		config.Secret, err = csrfConfigString(value, "secret")
		if err != nil {
			return CSRFConfig{}, err
		}
	}
	if value, exists := raw["safe_methods"]; exists {
		config.SafeMethods, err = csrfConfigMethods(value)
		if err != nil {
			return CSRFConfig{}, err
		}
	}
	if err = validateCSRFConfig(config); err != nil {
		return CSRFConfig{}, err
	}
	config.SafeMethods = append([]string(nil), config.SafeMethods...)
	return config, nil
}

// Csrf 创建使用默认配置和进程级随机密钥的签名双重提交中间件。
func Csrf() (Handler, error) {
	return CsrfWithConfig(DefaultCSRFConfig())
}

// CsrfWithConfig 创建不可变 CSRF 中间件；空 Secret 会生成进程级随机密钥。
func CsrfWithConfig(config CSRFConfig) (Handler, error) {
	if err := validateCSRFConfig(config); err != nil {
		return nil, err
	}
	config.SafeMethods = append([]string(nil), config.SafeMethods...)
	if config.Secret == "" {
		secret := make([]byte, minCSRFSecretBytes)
		if _, err := io.ReadFull(rand.Reader, secret); err != nil {
			return nil, errors.Join(ErrCSRFTokenGeneration, err)
		}
		config.Secret = string(secret)
	}
	middleware := &csrfMiddleware{
		config:      config,
		safeMethods: make(map[string]struct{}, len(config.SafeMethods)),
		random:      rand.Reader,
		now:         time.Now,
	}
	for _, method := range config.SafeMethods {
		middleware.safeMethods[method] = struct{}{}
	}
	return middleware.handle, nil
}

func (m *csrfMiddleware) handle(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
	if m == nil || req == nil || req.Raw() == nil || next == nil {
		return csrfErrorResponse(http.StatusInternalServerError, "CSRF 服务不可用")
	}
	method := strings.ToUpper(req.Method())
	cookieToken, found, cookieErr := readCSRFCookie(req.Raw(), m.config.CookieName)
	if _, safe := m.safeMethods[method]; safe {
		if errors.Is(cookieErr, ErrDuplicateCSRFCookie) {
			return csrfErrorResponse(http.StatusBadRequest, "CSRF Cookie 重复")
		}
		valid := cookieErr == nil && found
		if valid {
			_, cookieErr = verifyCSRFToken(
				m.config.CookieName, cookieToken, m.config.Secret, m.config.MaxAge, m.now(),
			)
			valid = cookieErr == nil
		}
		response := next(req)
		if response == nil || valid {
			return response
		}
		signed, err := m.newSignedToken()
		if err != nil {
			return csrfErrorResponse(http.StatusInternalServerError, "CSRF token 生成失败")
		}
		if err = setCSRFCookie(response, req, m.config, signed, m.now()); err != nil {
			return csrfErrorResponse(http.StatusInternalServerError, "CSRF Cookie 写入失败")
		}
		return response
	}

	if cookieErr != nil || !found {
		return csrfErrorResponse(http.StatusForbidden, "CSRF token 校验失败")
	}
	if _, err := verifyCSRFToken(
		m.config.CookieName, cookieToken, m.config.Secret, m.config.MaxAge, m.now(),
	); err != nil {
		return csrfErrorResponse(http.StatusForbidden, "CSRF token 校验失败")
	}
	submitted, err := submittedCSRFToken(req.Raw(), m.config)
	if err != nil || submitted == "" || len(submitted) > maxCSRFTokenBytes ||
		subtle.ConstantTimeCompare([]byte(cookieToken), []byte(submitted)) != 1 {
		return csrfErrorResponse(http.StatusForbidden, "CSRF token 校验失败")
	}
	return next(req)
}

func (m *csrfMiddleware) newSignedToken() (string, error) {
	nonce, err := generateCSRFToken(m.random)
	if err != nil {
		return "", err
	}
	return signCSRFToken(m.config.CookieName, nonce, m.config.Secret, m.now())
}

func generateCSRFToken(random io.Reader) (string, error) {
	if random == nil {
		return "", ErrCSRFTokenGeneration
	}
	buffer := make([]byte, csrfTokenBytes)
	if _, err := io.ReadFull(random, buffer); err != nil {
		return "", errors.Join(ErrCSRFTokenGeneration, err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func signCSRFToken(cookieName, nonce, secret string, now time.Time) (string, error) {
	decodedNonce, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(decodedNonce) != csrfTokenBytes || cookieName == "" || len(secret) < minCSRFSecretBytes {
		return "", ErrInvalidCSRFToken
	}
	if base64.RawURLEncoding.EncodeToString(decodedNonce) != nonce {
		return "", ErrInvalidCSRFToken
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(cookieName + "\x00v1\x00" + nonce + "\x00" + timestamp))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return "v1." + nonce + "." + timestamp + "." + signature, nil
}

func verifyCSRFToken(cookieName, token, secret string, maxAge int, now time.Time) (string, error) {
	if len(token) == 0 || len(token) > maxCSRFTokenBytes || cookieName == "" ||
		len(secret) < minCSRFSecretBytes || maxAge <= 0 {
		return "", ErrInvalidCSRFToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return "", ErrInvalidCSRFToken
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(nonce) != csrfTokenBytes {
		return "", ErrInvalidCSRFToken
	}
	if base64.RawURLEncoding.EncodeToString(nonce) != parts[1] {
		return "", ErrInvalidCSRFToken
	}
	timestamp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", ErrInvalidCSRFToken
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(provided) != sha256.Size {
		return "", ErrInvalidCSRFToken
	}
	if base64.RawURLEncoding.EncodeToString(provided) != parts[3] {
		return "", ErrInvalidCSRFToken
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(cookieName + "\x00v1\x00" + parts[1] + "\x00" + parts[2]))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return "", ErrInvalidCSRFToken
	}
	signedAt := time.Unix(timestamp, 0)
	if signedAt.After(now.Add(maxCSRFClockSkew)) {
		return "", ErrInvalidCSRFToken
	}
	if now.Sub(signedAt) > time.Duration(maxAge)*time.Second {
		return "", ErrExpiredCSRFToken
	}
	return parts[1], nil
}

func readCSRFCookie(raw *http.Request, name string) (string, bool, error) {
	if raw == nil {
		return "", false, ErrInvalidCSRFToken
	}
	value := ""
	found := false
	for _, item := range raw.Cookies() {
		if item.Name != name {
			continue
		}
		if found {
			return "", false, ErrDuplicateCSRFCookie
		}
		if len(item.Value) > maxCSRFTokenBytes {
			return "", false, ErrInvalidCSRFToken
		}
		value = item.Value
		found = true
	}
	return value, found, nil
}

func submittedCSRFToken(raw *http.Request, config CSRFConfig) (string, error) {
	values := raw.Header.Values(config.HeaderName)
	if len(values) > 1 || len(values) == 1 && strings.Contains(values[0], ",") {
		return "", ErrInvalidCSRFToken
	}
	if len(values) == 1 && values[0] != "" {
		return values[0], nil
	}
	mediaType, _, err := mime.ParseMediaType(raw.Header.Get("Content-Type"))
	if err != nil && raw.Header.Get("Content-Type") != "" {
		return "", err
	}
	switch strings.ToLower(mediaType) {
	case "application/x-www-form-urlencoded":
		if err = raw.ParseForm(); err != nil {
			return "", err
		}
		return singleCSRFFormValue(raw.PostForm[config.FieldName])
	case "multipart/form-data":
		if err = raw.ParseMultipartForm(context.DefaultMultipartMemoryLimit); err != nil {
			return "", err
		}
		if raw.MultipartForm == nil {
			return "", nil
		}
		return singleCSRFFormValue(raw.MultipartForm.Value[config.FieldName])
	default:
		return "", nil
	}
}

func singleCSRFFormValue(values []string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", ErrInvalidCSRFToken
	}
	return values[0], nil
}

func setCSRFCookie(response *context.Response, req *context.Request, config CSRFConfig, token string, now time.Time) error {
	if response == nil || len(token) == 0 || len(token) > maxCSRFTokenBytes {
		return ErrInvalidCSRFToken
	}
	sameSite := http.SameSiteLaxMode
	if config.SameSite == "Strict" {
		sameSite = http.SameSiteStrictMode
	} else if config.SameSite == "None" {
		sameSite = http.SameSiteNoneMode
	}
	written := &http.Cookie{
		Name: config.CookieName, Value: token, Path: config.CookiePath, Domain: config.CookieDomain,
		MaxAge: config.MaxAge, Expires: now.Add(time.Duration(config.MaxAge) * time.Second),
		Secure: config.Secure || req.IsSsl() || config.SameSite == "None", HttpOnly: false,
		SameSite: sameSite,
	}
	if err := written.Valid(); err != nil {
		return err
	}
	header := written.String()
	if header == "" || len(header) > maxCSRFCookieBytes {
		return ErrInvalidCSRFToken
	}
	response.AddHeader("Set-Cookie", header)
	return nil
}

func validateCSRFConfig(config CSRFConfig) error {
	if config.CookieName == "" || !utf8.ValidString(config.CookieName) || hasCSRFControl(config.CookieName) {
		return invalidCSRFConfig("cookie_name", "为空或包含非法字符")
	}
	if !isValidCSRFHeaderName(config.HeaderName) {
		return invalidCSRFConfig("header_name", "不是合法 HTTP 头名")
	}
	if !isValidCSRFFieldName(config.FieldName) {
		return invalidCSRFConfig("field_name", "仅允许字母、数字、下划线、点和连字符")
	}
	if config.MaxAge < 1 || config.MaxAge > maxCSRFAgeSeconds {
		return invalidCSRFConfig("max_age", "超出允许范围")
	}
	if config.Secret != "" && (len(config.Secret) < minCSRFSecretBytes || len(config.Secret) > maxCSRFSecretBytes) {
		return invalidCSRFConfig("secret", "非空密钥必须为 32 至 4096 字节")
	}
	normalized, err := normalizeCSRFSameSite(config.SameSite)
	if err != nil || normalized != config.SameSite {
		return invalidCSRFConfig("samesite", "必须是规范形式 Lax、Strict 或 None")
	}
	probe := &http.Cookie{
		Name: config.CookieName, Value: "x", Path: config.CookiePath, Domain: config.CookieDomain,
	}
	if err = probe.Valid(); err != nil || config.CookiePath == "" || !strings.HasPrefix(config.CookiePath, "/") ||
		strings.Contains(config.CookiePath, ";") || hasCSRFControl(config.CookiePath) {
		return invalidCSRFConfig("cookie_path/domain", fmt.Sprintf("Cookie 策略非法: %v", err))
	}
	if len(config.SafeMethods) == 0 {
		return invalidCSRFConfig("safe_methods", "不能为空")
	}
	seen := make(map[string]bool, len(config.SafeMethods))
	for _, method := range config.SafeMethods {
		if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
			return invalidCSRFConfig("safe_methods", fmt.Sprintf("%s 不是允许的安全方法", method))
		}
		if seen[method] {
			return invalidCSRFConfig("safe_methods", fmt.Sprintf("%s 重复", method))
		}
		seen[method] = true
	}
	return nil
}

func normalizeCSRFSameSite(value string) (string, error) {
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

func csrfConfigMethods(value interface{}) ([]string, error) {
	methods := make([]string, 0)
	switch typed := value.(type) {
	case []string:
		methods = append(methods, typed...)
	case []interface{}:
		for _, item := range typed {
			method, ok := item.(string)
			if !ok {
				return nil, invalidCSRFConfig("safe_methods", "每一项必须是字符串")
			}
			methods = append(methods, method)
		}
	default:
		return nil, invalidCSRFConfig("safe_methods", "必须是字符串数组")
	}
	return methods, nil
}

func csrfConfigString(value interface{}, field string) (string, error) {
	text, ok := value.(string)
	if !ok || !utf8.ValidString(text) || hasCSRFControl(text) {
		return "", invalidCSRFConfig(field, "必须是不含控制字符的字符串")
	}
	return text, nil
}

func csrfConfigBool(value interface{}, field string) (bool, error) {
	parsed, ok := value.(bool)
	if !ok {
		return false, invalidCSRFConfig(field, "必须是布尔值")
	}
	return parsed, nil
}

func csrfConfigInteger(value interface{}, field string, minimum, maximum int) (int, error) {
	parsed, ok := exactCSRFInteger(value)
	if !ok || parsed < int64(minimum) || parsed > int64(maximum) {
		return 0, invalidCSRFConfig(field, fmt.Sprintf("必须是 %d 至 %d 的整数", minimum, maximum))
	}
	return int(parsed), nil
}

func exactCSRFInteger(value interface{}) (int64, bool) {
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
		return exactCSRFFloat(float64(typed))
	case float64:
		return exactCSRFFloat(typed)
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func exactCSRFFloat(value float64) (int64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
		value < float64(math.MinInt64) || value >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(value), true
}

func isValidCSRFFieldName(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		isLetter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		isDigit := character >= '0' && character <= '9'
		if isLetter || isDigit || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func isValidCSRFHeaderName(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		isLetter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		isDigit := character >= '0' && character <= '9'
		if isLetter || isDigit || strings.ContainsRune("!#$%&'*+-.^_`|~", character) {
			continue
		}
		return false
	}
	return true
}

func hasCSRFControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func invalidCSRFConfig(field, reason string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidCSRFConfig, field, reason)
}

func csrfErrorResponse(status int, message string) *context.Response {
	return context.NewResponse().Code(status).Json(map[string]interface{}{
		"code": status,
		"msg":  message,
		"data": nil,
	})
}
