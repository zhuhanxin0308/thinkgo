package connector

import (
	"context"
	"database/sql"
	"net/url"
	"time"

	"thinkgo/framework/db"
	"thinkgo/framework/db/builder"

	_ "github.com/microsoft/go-mssqldb"
)

// Sqlsrv connector
type Sqlsrv struct{}

// Connect connects to SQLServer
func (s *Sqlsrv) Connect(config db.Config) (db.Connection, error) {
	dsn := buildSqlsrvDSN(config)
	conn, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &db.SQLConnection{DB: conn, Builder: &builder.Sqlsrv{}}, nil
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

func init() {
	db.RegisterConnector("sqlsrv", &Sqlsrv{})
}
