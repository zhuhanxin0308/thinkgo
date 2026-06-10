package connector

import (
	"database/sql"
	"fmt"
	"thinkgo/framework/db"
	"thinkgo/framework/db/builder"

	_ "github.com/lib/pq"
)

// Pgsql connector
type Pgsql struct{}

// Connect connects to PostgreSQL
func (p *Pgsql) Connect(config db.Config) (db.Connection, error) {
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		config.Hostname,
		config.Hostport,
		config.Username,
		config.Password,
		config.Database,
	)
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	return &db.SQLConnection{DB: conn, Builder: &builder.Pgsql{}}, nil
}

func init() {
	db.RegisterConnector("pgsql", &Pgsql{})
}
