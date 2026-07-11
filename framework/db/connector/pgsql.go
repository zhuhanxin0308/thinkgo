package connector

import (
	"context"
	"database/sql"
	"net/url"
	"time"

	"thinkgo/framework/db"
	"thinkgo/framework/db/builder"

	_ "github.com/lib/pq"
)

// Pgsql connector
type Pgsql struct{}

// Connect connects to PostgreSQL
func (p *Pgsql) Connect(config db.Config) (db.Connection, error) {
	dsn := buildPgsqlDSN(config)
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &db.SQLConnection{DB: conn, Builder: &builder.Pgsql{}}, nil
}

// buildPgsqlDSN 使用 URL 结构构造连接串，避免账号密码中的特殊字符篡改参数。
func buildPgsqlDSN(config db.Config) string {
	query := url.Values{}
	query.Set("sslmode", "require")
	for key, value := range config.Params {
		query.Set(key, value)
	}

	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(config.Username, config.Password),
		Host:     joinHostPort(config.Hostname, config.Hostport),
		Path:     config.Database,
		RawQuery: query.Encode(),
	}
	return dsn.String()
}

func init() {
	db.RegisterConnector("pgsql", &Pgsql{})
}
