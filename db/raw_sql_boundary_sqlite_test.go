//go:build cgo

package db_test

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

func TestRawSQLBoundarySQLite(t *testing.T) {
	raw, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if err := raw.Ping(); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	database := db.NewDB(db.NewSQLConnection(raw, &builder.Sqlite{}))
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	exerciseRawSQLBoundary(t, database, "raw_boundary_records")
}
