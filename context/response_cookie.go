package context

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Cookie 设置 Cookie。省略 option 时使用 ThinkPHP 默认的会话 Cookie 和根路径；
// option 可传过期秒数或包含 expire、path、domain、secure、httponly、samesite 的映射。
// 为兼容已有 Go 调用，仍接受 maxAge、path、domain、secure、httpOnly 五个位置参数。
func (r *Response) Cookie(name, value string, options ...interface{}) *Response {
	if r == nil {
		return nil
	}
	cookie := &http.Cookie{Name: name, Value: value, Path: "/"} // #nosec G124 -- ThinkPHP 默认 Cookie 策略允许业务通过选项决定 Secure、HttpOnly 与 SameSite。
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
	for rawName, value := range values {
		switch strings.ToLower(strings.TrimSpace(rawName)) {
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
