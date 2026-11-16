package deploy

import (
	"context"
	"fmt"
	"maps"
	"slices"

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
		if connection["type"] != "pgsql" || connection["params"] == nil {
			continue
		}
		params, ok := connection["params"].(map[string]interface{})
		// 未覆盖 sslmode 时，PostgreSQL 连接器使用 verify-full。
		mode, explicit := params["sslmode"]
		if !ok || explicit && mode != "verify-full" {
			return fail(fmt.Sprintf("PostgreSQL 连接 %q 的生产配置必须使用 sslmode=verify-full", name))
		}
	}
	return pass("PostgreSQL 连接配置均符合生产 TLS 模式要求")
}
