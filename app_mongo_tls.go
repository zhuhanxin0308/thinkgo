package framework

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

// ValidateMongoProductionTLS 使用启动期相同的参数契约审查发布配置。
func ValidateMongoProductionTLS(rawParams interface{}) error {
	params, err := readDatabaseParams(rawParams)
	if err != nil {
		return err
	}
	return validateMongoProductionTLS(params)
}

func validateMongoProductionTLS(params map[string]string) error {
	hasTLSOption := false
	for _, name := range slices.Sorted(maps.Keys(params)) {
		key := strings.TrimSpace(name)
		if key == "" || key != name {
			return fmt.Errorf("%w: MongoDB 连接参数名非法", db.ErrInvalidDatabaseConfig)
		}
		value := params[name]
		switch strings.ToLower(key) {
		case "tls", "ssl":
			if hasTLSOption {
				return fmt.Errorf("%w: MongoDB TLS 参数重复", db.ErrInvalidDatabaseConfig)
			}
			if !strings.EqualFold(strings.TrimSpace(value), "true") {
				return fmt.Errorf("%w: MongoDB 生产配置必须启用 TLS", db.ErrInvalidDatabaseConfig)
			}
			hasTLSOption = true
		case "tlsinsecure", "sslinsecure",
			"tlsallowinvalidcertificates", "sslallowinvalidcertificates",
			"tlsallowinvalidhostnames", "sslallowinvalidhostnames",
			"tlsdisablecertificaterevocationcheck", "tlsdisableocspendpointcheck":
			if value != "false" {
				return fmt.Errorf("%w: MongoDB 生产配置必须校验证书和主机名，参数 %q 必须为 false", db.ErrInvalidDatabaseConfig, key)
			}
		}
	}
	return nil
}
