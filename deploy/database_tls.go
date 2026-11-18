package deploy

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3"
)

func checkDatabaseTLS(_ context.Context, app *framework.App) Result {
	configuration, err := applicationConfig(app)
	if err != nil {
		return fail(err.Error())
	}
	if databasePolicyDisabled(app) {
		return pass("数据库启动策略为 disabled，跳过 TLS 配置检查")
	}
	connections := configuration.GetMap("database.connections")
	for _, name := range slices.Sorted(maps.Keys(connections)) {
		connection, _ := connections[name].(map[string]interface{})
		kind, _ := connection["type"].(string)
		if strings.EqualFold(strings.TrimSpace(kind), "mongo") {
			if err := framework.ValidateMongoProductionTLS(connection["params"]); err != nil {
				return fail(fmt.Sprintf("MongoDB 连接 %q 的生产 TLS 校验失败: %v", name, err))
			}
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(kind), "pgsql") || connection["params"] == nil {
			continue
		}
		if !validPostgreSQLProductionTLSParams(connection["params"]) {
			return fail(fmt.Sprintf("PostgreSQL 连接 %q 的生产配置必须使用 sslmode=verify-full", name))
		}
	}
	return pass("PostgreSQL 和 MongoDB 连接配置均符合生产 TLS 模式要求")
}

// validPostgreSQLProductionTLSParams 兼容 JSON 动态映射与 Config.Set 写入的 map[string]string。
func validPostgreSQLProductionTLSParams(raw interface{}) bool {
	switch params := raw.(type) {
	case map[string]string:
		for key, value := range params {
			if strings.TrimSpace(key) == "" {
				return false
			}
			if strings.EqualFold(strings.TrimSpace(key), "sslmode") && value != "verify-full" {
				return false
			}
		}
		return true
	case map[string]interface{}:
		for key, rawValue := range params {
			if strings.TrimSpace(key) == "" {
				return false
			}
			value, ok := rawValue.(string)
			if !ok {
				return false
			}
			if strings.EqualFold(strings.TrimSpace(key), "sslmode") && value != "verify-full" {
				return false
			}
		}
		return true
	default:
		return false
	}
}
