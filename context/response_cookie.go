package context

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type setCookieIdentity struct {
	name        string
	domain      string
	path        string
	partitioned bool
}

func parseSetCookieIdentity(raw string) (setCookieIdentity, bool) {
	parsed, err := http.ParseSetCookie(raw)
	if err != nil || parsed == nil || parsed.Name == "" {
		return setCookieIdentity{}, false
	}
	domain := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(parsed.Domain), "."))
	return setCookieIdentity{
		name:        parsed.Name,
		domain:      domain,
		path:        parsed.Path,
		partitioned: parsed.Partitioned,
	}, true
}

func mergeSetCookieHeaderValues(header http.Header, values []string) {
	hasNonCanonical := false
	existingCount := len(header["Set-Cookie"])
	for name, items := range header {
		if name != "Set-Cookie" && strings.EqualFold(name, "Set-Cookie") {
			hasNonCanonical = true
			existingCount += len(items)
		}
	}
	if existingCount == 0 && !hasNonCanonical {
		for _, value := range values {
			header.Add("Set-Cookie", value)
		}
		return
	}
	existing := make([]string, 0, existingCount)
	existing = append(existing, header["Set-Cookie"]...)
	if hasNonCanonical {
		for name, items := range header {
			if name != "Set-Cookie" && strings.EqualFold(name, "Set-Cookie") {
				existing = append(existing, items...)
				delete(header, name)
			}
		}
	}
	if len(values) == 0 {
		header["Set-Cookie"] = existing
		return
	}
	overriddenRaw := make(map[string]struct{}, len(values))
	overriddenCookies := make(map[setCookieIdentity]struct{}, len(values))
	for _, value := range values {
		overriddenRaw[value] = struct{}{}
		if identity, ok := parseSetCookieIdentity(value); ok {
			overriddenCookies[identity] = struct{}{}
		}
	}
	header.Del("Set-Cookie")
	for _, item := range existing {
		if _, duplicate := overriddenRaw[item]; duplicate {
			continue
		}
		if identity, ok := parseSetCookieIdentity(item); ok {
			if _, overridden := overriddenCookies[identity]; overridden {
				continue
			}
		}
		header.Add("Set-Cookie", item)
	}
	for _, value := range values {
		header.Add("Set-Cookie", value)
	}
}

// Cookie 设置 Cookie，默认使用根路径、HttpOnly 和 SameSite=Lax。
// option 可传过期秒数或包含 expire、path、domain、secure、httponly、samesite 的映射。
// 为兼容已有 Go 调用，仍接受 maxAge、path、domain、secure、httpOnly 五个位置参数。
func (r *Response) Cookie(name, value string, options ...interface{}) *Response {
	if r == nil {
		return nil
	}
	cookie := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode} // #nosec G124 -- 允许调用方显式选择 Secure 策略。
	if err := applyResponseCookieOptions(cookie, options); err != nil {
		r.addError(fmt.Errorf("%w: Cookie option: %v", ErrInvalidResponseHeader, err))
		return r
	}
	// #nosec G124 -- 该兼容 API 接受调用方明确传入的 Secure/HttpOnly 策略，并校验 Cookie 格式。
	if err := cookie.Valid(); err != nil {
		r.addError(fmt.Errorf("%w: Cookie: %v", ErrInvalidResponseHeader, err))
		return r
	}
	r.AddHeader("Set-Cookie", cookie.String())
	return r
}

// #nosec G124 -- 该兼容层必须接受 ThinkPHP Cookie 选项中的显式安全策略。
func applyResponseCookieOptions(cookie *http.Cookie, options []interface{}) error {
	if cookie == nil {
		return errors.New("Cookie 不能为空")
	}
	if len(options) == 0 || len(options) == 1 && options[0] == nil {
		return nil
	}
	if len(options) == 5 {
		expire, expireOK := responseCookieExpireSeconds(options[0])
		path, pathOK := options[1].(string)
		domain, domainOK := options[2].(string)
		secure, secureOK := options[3].(bool)
		httpOnly, httpOnlyOK := options[4].(bool)
		if !expireOK || !pathOK || !domainOK || !secureOK || !httpOnlyOK {
			return errors.New("旧式 Cookie 参数类型非法")
		}
		cookie.Path = path
		cookie.Domain = domain
		cookie.Secure = secure
		cookie.HttpOnly = httpOnly
		return setResponseCookieExpire(cookie, expire)
	}
	if len(options) != 1 {
		return fmt.Errorf("参数数量 %d 非法", len(options))
	}
	if expire, ok := responseCookieExpireSeconds(options[0]); ok {
		return setResponseCookieExpire(cookie, expire)
	}
	if expires, ok := options[0].(time.Time); ok {
		cookie.Expires = expires
		return nil
	}
	switch values := options[0].(type) {
	case map[string]interface{}:
		return applyResponseCookieOptionMap(cookie, values)
	case map[string]string:
		converted := make(map[string]interface{}, len(values))
		for name, value := range values {
			converted[name] = value
		}
		return applyResponseCookieOptionMap(cookie, converted)
	default:
		return fmt.Errorf("不支持类型 %T", options[0])
	}
}

// #nosec G124 -- 映射值来自 ThinkPHP 兼容配置，调用方可显式设置 Secure、HttpOnly 与 SameSite。
func applyResponseCookieOptionMap(cookie *http.Cookie, values map[string]interface{}) error {
	seen := make(map[string]struct{}, len(values))
	for rawName, value := range values {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("重复的 Cookie 选项 %q", name)
		}
		seen[name] = struct{}{}
		switch name {
		case "expire":
			if expires, ok := value.(time.Time); ok {
				cookie.Expires = expires
				continue
			}
			expire, ok := responseCookieExpireSeconds(value)
			if !ok {
				return fmt.Errorf("expire 类型 %T 非法", value)
			}
			if err := setResponseCookieExpire(cookie, expire); err != nil {
				return err
			}
		case "path":
			path, ok := value.(string)
			if !ok {
				return fmt.Errorf("path 类型 %T 非法", value)
			}
			cookie.Path = path
		case "domain":
			domain, ok := value.(string)
			if !ok {
				return fmt.Errorf("domain 类型 %T 非法", value)
			}
			cookie.Domain = domain
		case "secure":
			secure, ok := value.(bool)
			if !ok {
				return fmt.Errorf("secure 类型 %T 非法", value)
			}
			cookie.Secure = secure
		case "httponly":
			httpOnly, ok := value.(bool)
			if !ok {
				return fmt.Errorf("httponly 类型 %T 非法", value)
			}
			cookie.HttpOnly = httpOnly
		case "samesite":
			sameSite, ok := value.(string)
			if !ok {
				return fmt.Errorf("samesite 类型 %T 非法", value)
			}
			parsed, err := parseResponseCookieSameSite(sameSite)
			if err != nil {
				return err
			}
			cookie.SameSite = parsed
		default:
			return fmt.Errorf("未知 Cookie 选项 %q", rawName)
		}
	}
	return nil
}

func responseCookieExpireSeconds(value interface{}) (int64, bool) {
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
		if uint64(typed) <= uint64(^uint64(0)>>1) {
			return int64(typed), true
		}
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed <= uint64(^uint64(0)>>1) {
			return int64(typed), true
		}
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	}
	return 0, false
}

// #nosec G124 -- 仅调整 ThinkPHP expire 语义，不负责覆盖调用方选择的 Cookie 安全策略。
func setResponseCookieExpire(cookie *http.Cookie, seconds int64) error {
	if seconds == 0 {
		cookie.Expires = time.Time{}
		return nil
	}
	const maxDurationSeconds = int64(^uint64(0)>>1) / int64(time.Second)
	if seconds > maxDurationSeconds || seconds < -maxDurationSeconds {
		return errors.New("expire 超出支持范围")
	}
	cookie.Expires = time.Now().Add(time.Duration(seconds) * time.Second)
	return nil
}

func parseResponseCookieSameSite(value string) (http.SameSite, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return http.SameSiteDefaultMode, nil
	case "lax":
		return http.SameSiteLaxMode, nil
	case "strict":
		return http.SameSiteStrictMode, nil
	case "none":
		return http.SameSiteNoneMode, nil
	default:
		return http.SameSiteDefaultMode, fmt.Errorf("samesite %q 非法", value)
	}
}
