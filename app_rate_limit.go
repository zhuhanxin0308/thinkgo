package framework

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	"github.com/zhuhanxin0308/thinkgo/v3/ratelimit"
)

const (
	defaultRateLimitRate          = 100
	defaultRateLimitPeriodSeconds = 60
	defaultRateLimitBurst         = 100
	defaultRateLimitMaxKeys       = 100_000
	maximumRateLimitPeriodSeconds = 365 * 24 * 60 * 60
	maximumRateLimitKeys          = 10_000_000
)

func createAppRateLimit(values map[string]interface{}) (*middleware.RateLimit, bool, error) {
	allowed := map[string]struct{}{
		"enable": {}, "rate": {}, "period_seconds": {}, "burst": {}, "max_keys": {},
	}
	for key := range values {
		if _, exists := allowed[key]; !exists {
			return nil, false, fmt.Errorf("rate_limit 配置包含未知字段 %q", key)
		}
	}
	enabled, err := securityHeaderBool(values, "enable", false)
	if err != nil || !enabled {
		return nil, enabled, err
	}
	rate, err := rateLimitInt(values, "rate", defaultRateLimitRate)
	if err != nil {
		return nil, true, err
	}
	periodSeconds, err := rateLimitInt(values, "period_seconds", defaultRateLimitPeriodSeconds)
	if err != nil || periodSeconds <= 0 || periodSeconds > maximumRateLimitPeriodSeconds {
		if err == nil {
			err = fmt.Errorf("rate_limit.period_seconds 必须在 1 到 %d 之间", maximumRateLimitPeriodSeconds)
		}
		return nil, true, err
	}
	burst, err := rateLimitInt(values, "burst", defaultRateLimitBurst)
	if err != nil {
		return nil, true, err
	}
	maxKeys, err := rateLimitInt(values, "max_keys", defaultRateLimitMaxKeys)
	if err != nil || maxKeys <= 0 || maxKeys > maximumRateLimitKeys {
		if err == nil {
			err = fmt.Errorf("rate_limit.max_keys 必须在 1 到 %d 之间", maximumRateLimitKeys)
		}
		return nil, true, err
	}
	limit := ratelimit.Limit{Rate: rate, Period: time.Duration(periodSeconds) * time.Second, Burst: burst}
	if _, err = ratelimit.Validate(limit); err != nil {
		return nil, true, fmt.Errorf("rate_limit 策略非法: %w", err)
	}
	store, err := ratelimit.NewMemoryStore(maxKeys)
	if err != nil {
		return nil, true, err
	}
	handler, err := middleware.NewRateLimit(middleware.RateLimitConfig{Store: store, Limit: limit})
	return handler, true, err
}

func rateLimitInt(values map[string]interface{}, key string, fallback int) (int, error) {
	raw, exists := values[key]
	if !exists {
		return fallback, nil
	}
	switch value := raw.(type) {
	case int:
		return value, nil
	case int64:
		if strconv.IntSize == 32 && (value > int64(^uint(0)>>1) || value < -int64(^uint(0)>>1)-1) {
			return 0, fmt.Errorf("rate_limit.%s 超出整数范围", key)
		}
		return int(value), nil
	case json.Number:
		parsed, err := strconv.ParseInt(value.String(), 10, strconv.IntSize)
		if err != nil {
			return 0, fmt.Errorf("rate_limit.%s 必须是整数", key)
		}
		return int(parsed), nil
	default:
		return 0, fmt.Errorf("rate_limit.%s 必须是整数", key)
	}
}
