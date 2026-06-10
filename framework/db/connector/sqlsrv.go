package connector

import (
	"database/sql"
	"fmt"
	"thinkgo/framework/db"
	"thinkgo/framework/db/builder"

	_ "github.com/denisenkom/go-mssqldb"
)

// Sqlsrv connector
type Sqlsrv struct{}

// Connect connects to SQLServer
func (s *Sqlsrv) Connect(config db.Config) (db.Connection, error) {
	dsn := fmt.Sprintf("server=%s;user id=%s;password=%s;port=%s;database=%s",
		config.Hostname,
		config.Username,
		config.Password,
		config.Hostport,
		config.Database,
	)
	conn, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return nil, err
	}
	return &db.SQLConnection{DB: conn, Builder: &builder.Sqlsrv{}}, nil
}

func init() {
	db.RegisterConnector("sqlsrv", &Sqlsrv{})
}
