// +build oracle

package connector

import (
	"database/sql"
	"fmt"
	"thinkgo/framework/db"
	"thinkgo/framework/db/builder"

	_ "github.com/godror/godror"
)

// Oracle connector
type Oracle struct{}

// Connect connects to Oracle
func (o *Oracle) Connect(config db.Config) (db.Connection, error) {
	dsn := fmt.Sprintf("%s/%s@%s:%s/%s",
		config.Username,
		config.Password,
		config.Hostname,
		config.Hostport,
		config.Database,
	)
	conn, err := sql.Open("godror", dsn)
	if err != nil {
		return nil, err
	}
	return &db.SQLConnection{DB: conn, Builder: &builder.Oracle{}}, nil
}

func init() {
	db.RegisterConnector("oracle", &Oracle{})
}
