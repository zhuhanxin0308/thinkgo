package framework

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// createAppCors 严格解析应用 CORS 配置；禁用全局管道时仍构造别名并校验完整策略。
func createAppCors(values map[string]interface{}) (middleware.Handler, bool, error) {
	allowed := map[string]struct{}{
		"enable": {}, "allow_origins": {}, "allow_methods": {}, "allow_headers": {},
		"expose_headers": {}, "allow_credentials": {}, "max_age": {},
	}
	for key := range values {
		if _, exists := allowed[key]; !exists {
			return nil, false, fmt.Errorf("cors 配置包含未知字段 %q", key)
		}
	}
	defaults := middleware.DefaultCorsConfig()
	enabled, err := corsConfigBool(values, "enable", false)
	if err != nil {
		return nil, false, err
	}
	config := middleware.CorsConfig{}
	if config.AllowOrigins, err = corsConfigStrings(values, "allow_origins", defaults.AllowOrigins); err != nil {
		return nil, enabled, err
	}
	if config.AllowMethods, err = corsConfigStrings(values, "allow_methods", defaults.AllowMethods); err != nil {
		return nil, enabled, err
	}
	if config.AllowHeaders, err = corsConfigStrings(values, "allow_headers", defaults.AllowHeaders); err != nil {
		return nil, enabled, err
	}
	if config.ExposeHeaders, err = corsConfigStrings(values, "expose_headers", defaults.ExposeHeaders); err != nil {
		return nil, enabled, err
	}
	if config.AllowCredentials, err = corsConfigBool(values, "allow_credentials", defaults.AllowCredentials); err != nil {
		return nil, enabled, err
	}
	if config.MaxAge, err = corsConfigInt(values, "max_age", defaults.MaxAge); err != nil {
		return nil, enabled, err
	}
	handler, err := middleware.NewCors(config)
	return handler, enabled, err
}

func corsConfigBool(values map[string]interface{}, key string, fallback bool) (bool, error) {
	raw, exists := values[key]
	if !exists {
		return fallback, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("cors.%s 必须是布尔值", key)
	}
	return value, nil
}

func corsConfigStrings(values map[string]interface{}, key string, fallback []string) ([]string, error) {
	raw, exists := values[key]
	if !exists {
		return append([]string(nil), fallback...), nil
	}
	switch typed := raw.(type) {
	case []string:
		return append([]string(nil), typed...), nil
	case []interface{}:
		result := make([]string, len(typed))
		for index, value := range typed {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("cors.%s[%d] 必须是字符串", key, index)
			}
			result[index] = text
		}
		return result, nil
	default:
		return nil, fmt.Errorf("cors.%s 必须是字符串数组", key)
	}
}

func corsConfigInt(values map[string]interface{}, key string, fallback int) (int, error) {
	raw, exists := values[key]
	if !exists {
		return fallback, nil
	}
	switch value := raw.(type) {
	case int:
		return value, nil
	case int64:
		if strconv.IntSize == 32 && (value > int64(^uint(0)>>1) || value < -int64(^uint(0)>>1)-1) {
			return 0, fmt.Errorf("cors.%s 超出整数范围", key)
		}
		return int(value), nil
	case json.Number:
		parsed, err := strconv.ParseInt(value.String(), 10, strconv.IntSize)
		if err != nil {
			return 0, fmt.Errorf("cors.%s 必须是整数", key)
		}
		return int(parsed), nil
	default:
		return 0, fmt.Errorf("cors.%s 必须是整数", key)
	}
}

// unavailableCorsHandler 只用于保持别名非空，启动错误仍会阻止应用进入服务状态。
func unavailableCorsHandler(*context.Request, func(*context.Request) *context.Response) *context.Response {
	return context.NewResponse().Code(http.StatusInternalServerError).Content(http.StatusText(http.StatusInternalServerError))
}
