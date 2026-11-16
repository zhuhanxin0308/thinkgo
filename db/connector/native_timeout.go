package connector

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

const (
	nativeOperationTimeoutOption  = "operation_timeout"
	defaultNativeOperationTimeout = 10 * time.Second
)

// takeNativeOperationTimeout 读取框架选项，避免将其透传给数据库驱动。
func takeNativeOperationTimeout(config db.Config) (db.Config, time.Duration, error) {
	optionKey := ""
	for key := range config.Params {
		if !strings.EqualFold(key, nativeOperationTimeoutOption) {
			continue
		}
		if optionKey != "" {
			return db.Config{}, 0, fmt.Errorf("%w: %s 不能重复配置", db.ErrInvalidDatabaseConfig, nativeOperationTimeoutOption)
		}
		optionKey = key
	}
	if optionKey == "" {
		return config, defaultNativeOperationTimeout, nil
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(config.Params[optionKey]))
	if err != nil || timeout <= 0 {
		return db.Config{}, 0, fmt.Errorf("%w: %s 必须是正的时长，例如 30s", db.ErrInvalidDatabaseConfig, nativeOperationTimeoutOption)
	}
	config.Params = maps.Clone(config.Params)
	delete(config.Params, optionKey)
	return config, timeout, nil
}
