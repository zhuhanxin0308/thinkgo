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
	handle, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	handle.SetMaxOpenConns(1)
	database := db.NewDB(db.NewSQLConnection(handle, &builder.Sqlite{}))
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := database.Execute("CREATE TABLE literal_names (id INTEGER PRIMARY KEY, name TEXT NOT NULL, score INTEGER NOT NULL, active BOOLEAN NOT NULL, created_at DATETIME NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	assertLiteralLikeRows(t, database, "literal_names")
}
