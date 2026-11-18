//go:build oracle
// +build oracle

package connector

import (
	"database/sql"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"

	"github.com/godror/godror"
)

// Oracle 使用 godror 结构化参数，避免凭据通过字符串拼接破坏 DSN。
type Oracle struct{}

func (o *Oracle) Connect(config db.Config) (db.Connection, error) {
	validated, err := validateConnectorConfig(config, "oracle", true, true)
	if err != nil {
		return nil, err
	}
	params, err := buildOracleConnectionParams(validated)
	if err != nil {
		return nil, err
	}
	handle := sql.OpenDB(godror.NewConnector(params))
	return verifySQLConnection(handle, &builder.Oracle{}, validated)
}

func buildOracleConnectionParams(config db.Config) (godror.ConnectionParams, error) {
	connectString, err := buildOracleConnectString(config)
	if err != nil {
		return godror.ConnectionParams{}, err
	}
	var params godror.ConnectionParams
	params.Username = config.Username
	params.Password = godror.NewPassword(config.Password)
	params.ConnectString = connectString
	params.Charset = config.Charset
	return params, nil
}

func registerOracle() {
	mustRegisterConnector("oracle", &Oracle{})
}
