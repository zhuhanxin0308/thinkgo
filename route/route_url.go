package route

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// URL 根据命名路由生成路径，并严格拒绝复杂对象和非有限数值。
func (r *Router) URL(name string, params map[string]interface{}) (string, error) {
	r.mu.RLock()
	registered := r.cachedNames[name]
	r.mu.RUnlock()
	if registered == nil {
		if err := r.Freeze(); err != nil {
			return "", err
		}
		r.mu.RLock()
		registered = r.namedRoutes[name]
		r.mu.RUnlock()
	}
	if registered == nil {
		return "", fmt.Errorf("%w: 未找到命名路由 %q", ErrInvalidRouteParameter, name)
	}

	consumed := make(map[string]bool)
	pathParts := make([]string, 0, len(registered.pathParts))
	for _, part := range registered.pathParts {
		if part.param == "" {
			pathParts = append(pathParts, url.PathEscape(part.literal))
			continue
		}
		value, exists := params[part.param]
		if !exists || value == nil {
			if part.optional {
				continue
			}
			return "", fmt.Errorf("%w: 路由 %q 缺少参数 %q", ErrInvalidRouteParameter, name, part.param)
		}
		text, valid := scalarRouteParameter(value)
		if !valid || text == "" {
			if part.optional && valid {
				consumed[part.param] = true
				continue
			}
			return "", fmt.Errorf("%w: 参数 %q 类型或值非法", ErrInvalidRouteParameter, part.param)
		}
		if !registered.validateParam(part.param, text) {
			return "", fmt.Errorf("%w: 参数 %q 不满足路由约束", ErrInvalidRouteParameter, part.param)
		}
		consumed[part.param] = true
		pathParts = append(pathParts, url.PathEscape(text))
	}

	if registered.ext != "" && len(pathParts) > 0 {
		if len(pathParts) == 0 {
			pathParts = append(pathParts, "."+registered.ext)
		} else {
			pathParts[len(pathParts)-1] += "." + registered.ext
		}
	}
	builtPath := "/" + joinEscapedPathParts(pathParts)
	if len(pathParts) == 0 {
		builtPath = "/"
	}

	queryKeys := make([]string, 0, len(params))
	for key, value := range params {
		if consumed[key] || value == nil {
			continue
		}
		queryKeys = append(queryKeys, key)
	}
	if len(queryKeys) == 0 {
		return builtPath, nil
	}
	sort.Strings(queryKeys)
	query := make(url.Values)
	for _, key := range queryKeys {
		switch typed := params[key].(type) {
		case []string:
			for _, item := range typed {
				query.Add(key, item)
			}
		default:
			text, valid := scalarRouteParameter(typed)
			if !valid {
				return "", fmt.Errorf("%w: 查询参数 %q 类型非法", ErrInvalidRouteParameter, key)
			}
			query.Add(key, text)
		}
	}
	encodedQuery := query.Encode()
	if encodedQuery == "" {
		return builtPath, nil
	}
	return builtPath + "?" + encodedQuery, nil
}

// URLForRequest 为当前请求生成包含应用前缀的站内 URL。
func (r *Router) URLForRequest(request *fwcontext.Request, name string, params map[string]interface{}) (string, error) {
	path, err := r.URL(name, params)
	if err != nil || request == nil {
		return path, err
	}
	return request.ApplicationPath(path)
}

func joinEscapedPathParts(parts []string) string {
	result := ""
	for index, part := range parts {
		if index > 0 {
			result += "/"
		}
		result += part
	}
	return result
}

func scalarRouteParameter(value interface{}) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case bool:
		return strconv.FormatBool(typed), true
	case int:
		return strconv.FormatInt(int64(typed), 10), true
	case int8:
		return strconv.FormatInt(int64(typed), 10), true
	case int16:
		return strconv.FormatInt(int64(typed), 10), true
	case int32:
		return strconv.FormatInt(int64(typed), 10), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case uint:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint8:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint16:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint32:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint64:
		return strconv.FormatUint(typed, 10), true
	case float32:
		value := float64(typed)
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", false
		}
		return strconv.FormatFloat(value, 'g', -1, 32), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", false
		}
		return strconv.FormatFloat(typed, 'g', -1, 64), true
	case json.Number:
		raw := typed.String()
		if raw == "" || !json.Valid([]byte(raw)) || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
			return "", false
		}
		return raw, true
	default:
		return "", false
	}
}
