package framework

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

func createAppSecurityHeaders(values map[string]interface{}) (middleware.Handler, bool, error) {
	allowed := map[string]struct{}{
		"enable": {}, "content_security_policy": {}, "referrer_policy": {}, "permissions_policy": {},
		"cross_origin_opener_policy": {}, "cross_origin_resource_policy": {}, "frame_options": {},
		"hsts_max_age_seconds": {}, "hsts_include_subdomains": {}, "hsts_preload": {},
	}
	for key := range values {
		if _, exists := allowed[key]; !exists {
			return nil, false, fmt.Errorf("security_headers 配置包含未知字段 %q", key)
		}
	}
	enabled, err := securityHeaderBool(values, "enable", false)
	if err != nil || !enabled {
		return nil, enabled, err
	}
	config := middleware.SecurityHeadersConfig{}
	if config.ContentSecurityPolicy, err = securityHeaderString(values, "content_security_policy"); err != nil {
		return nil, true, err
	}
	if config.ReferrerPolicy, err = securityHeaderString(values, "referrer_policy"); err != nil {
		return nil, true, err
	}
	if config.PermissionsPolicy, err = securityHeaderString(values, "permissions_policy"); err != nil {
		return nil, true, err
	}
	if config.CrossOriginOpenerPolicy, err = securityHeaderString(values, "cross_origin_opener_policy"); err != nil {
		return nil, true, err
	}
	if config.CrossOriginResourcePolicy, err = securityHeaderString(values, "cross_origin_resource_policy"); err != nil {
		return nil, true, err
	}
	if config.FrameOptions, err = securityHeaderString(values, "frame_options"); err != nil {
		return nil, true, err
	}
	if config.HSTSMaxAgeSeconds, err = securityHeaderInt(values, "hsts_max_age_seconds", 0); err != nil {
		return nil, true, err
	}
	if config.HSTSIncludeSubDomains, err = securityHeaderBool(values, "hsts_include_subdomains", false); err != nil {
		return nil, true, err
	}
	if config.HSTSPreload, err = securityHeaderBool(values, "hsts_preload", false); err != nil {
		return nil, true, err
	}
	handler, err := middleware.NewSecurityHeaders(config)
	return handler, true, err
}

func securityHeaderString(values map[string]interface{}, key string) (string, error) {
	raw, exists := values[key]
	if !exists {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("security_headers.%s 必须是字符串", key)
	}
	return value, nil
}

func securityHeaderBool(values map[string]interface{}, key string, fallback bool) (bool, error) {
	raw, exists := values[key]
	if !exists {
		return fallback, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("security_headers.%s 必须是布尔值", key)
	}
	return value, nil
}

func securityHeaderInt(values map[string]interface{}, key string, fallback int) (int, error) {
	raw, exists := values[key]
	if !exists {
		return fallback, nil
	}
	switch value := raw.(type) {
	case int:
		return value, nil
	case int64:
		if value > int64(math.MaxInt) || value < int64(math.MinInt) {
			return 0, fmt.Errorf("security_headers.%s 超出整数范围", key)
		}
		return int(value), nil
	case json.Number:
		parsed, err := strconv.ParseInt(value.String(), 10, 32)
		if err != nil {
			return 0, fmt.Errorf("security_headers.%s 必须是整数", key)
		}
		return int(parsed), nil
	default:
		return 0, fmt.Errorf("security_headers.%s 必须是整数", key)
	}
}
