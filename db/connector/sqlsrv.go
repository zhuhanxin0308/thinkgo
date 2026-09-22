package connector

import (
	"net/url"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/db/builder"

	_ "github.com/microsoft/go-mssqldb"
)

// Sqlsrv connector
type Sqlsrv struct{}

// Connect connects to SQLServer
func (s *Sqlsrv) Connect(config db.Config) (db.Connection, error) {
	validated, err := validateConnectorConfig(config, "sqlsrv", true, true)
	if err != nil {
		return nil, err
	}
	return openSQLConnection("sqlserver", buildSqlsrvDSN(validated), &builder.Sqlsrv{}, validated)
}

// buildSqlsrvDSN 使用 URL DSN，避免分号拼接被密码等字段注入额外参数。
func buildSqlsrvDSN(config db.Config) string {
	query := url.Values{}
	query.Set("database", config.Database)
	query.Set("encrypt", "true")
	for key, value := range config.Params {
		query.Set(key, value)
	}

	dsn := url.URL{
		Scheme:   "sqlserver",
		User:     url.UserPassword(config.Username, config.Password),
		Host:     joinHostPort(config.Hostname, config.Hostport),
		RawQuery: query.Encode(),
	}
	return dsn.String()
}
