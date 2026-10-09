//go:build cgo

package db_test

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

func TestLiteralLikeSQLiteRowIsolation(t *testing.T) {
	for _, portable := range []bool{false, true} {
		name := "sql"
		if portable {
			name = "portable_connection"
		}
		t.Run(name, func(t *testing.T) {
			handle, err := sql.Open("sqlite3", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			handle.SetMaxOpenConns(1)
			sqlConnection := db.NewSQLConnection(handle, &builder.Sqlite{})
			// Hide optional SQL capabilities to verify the shared oracle uses only
			// the Connection contract also implemented by the native drivers.
			var connection db.Connection = sqlConnection
			if portable {
				connection = struct{ db.Connection }{sqlConnection}
			}
			database := db.NewDB(connection)
			t.Cleanup(func() {
				if err := database.Close(); err != nil {
					t.Error(err)
				}
			})
			// Schema creation belongs to the SQLite fixture, not the shared oracle.
			if _, err := sqlConnection.Execute("CREATE TABLE literal_names (id INTEGER PRIMARY KEY, name TEXT NOT NULL, score INTEGER NOT NULL, active BOOLEAN NOT NULL, created_at DATETIME NOT NULL)"); err != nil {
				t.Fatal(err)
			}
			assertLiteralLikeRows(t, database, "literal_names")
		})
	}
}
