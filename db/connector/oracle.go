//go:build oracle
// +build oracle

package connector

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/db/builder"

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
	protocol := "tcps"
	query := url.Values{}
	for key, value := range config.Params {
		if strings.EqualFold(key, "protocol") {
			protocol = strings.ToLower(strings.TrimSpace(value))
			continue
		}
		query.Set(key, value)
	}
	if protocol != "tcp" && protocol != "tcps" {
		return godror.ConnectionParams{}, fmt.Errorf("%w: Oracle protocol 只能是 tcp 或 tcps", db.ErrInvalidDatabaseConfig)
	}
	connectString := fmt.Sprintf("%s://%s/%s", protocol, joinHostPort(config.Hostname, config.Hostport), url.PathEscape(config.Database))
	if encoded := query.Encode(); encoded != "" {
		connectString += "?" + encoded
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
