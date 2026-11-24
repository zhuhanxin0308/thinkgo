//go:build oracle

package connector

import (
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

// TestOracleConnectorRejectsProtocolConflict 验证驱动参数与公开连接入口在创建 OCI 连接前都拒绝冲突。
func TestOracleConnectorRejectsProtocolConflict(t *testing.T) {
	config := db.Config{Type: "oracle", Hostname: "localhost", Database: "ORCL", Params: map[string]string{
		"protocol": "tcp", "PROTOCOL": " TCP ",
	}}
	params, err := buildOracleConnectionParams(config)
	if err != nil || params.ConnectString != "tcp://localhost/ORCL" {
		t.Fatalf("合法同义键未生成预期的驱动参数：%q，错误 %v", params.ConnectString, err)
	}
	config.Params["PROTOCOL"] = "tcps"
	params, err = buildOracleConnectionParams(config)
	if !errors.Is(err, db.ErrInvalidDatabaseConfig) || params.ConnectString != "" {
		t.Fatalf("冲突协议仍产生驱动参数：%q，错误 %v", params.ConnectString, err)
	}
	connection, err := (&Oracle{}).Connect(config)
	if connection != nil || !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("连接器未返回配置错误：连接 %v，错误 %v", connection, err)
	}
}
