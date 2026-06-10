package connector

import (
	"database/sql"
	"thinkgo/framework/db"
	"thinkgo/framework/db/builder"

	_ "github.com/mattn/go-sqlite3"
)

// Sqlite connector
type Sqlite struct{}

// Connect connects to SQLite
func (s *Sqlite) Connect(config db.Config) (db.Connection, error) {
	conn, err := sql.Open("sqlite3", config.Database)
	if err != nil {
		return nil, err
	}
	return &db.SQLConnection{DB: conn, Builder: &builder.Sqlite{}}, nil
}

func init() {
	db.RegisterConnector("sqlite", &Sqlite{})
}
