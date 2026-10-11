//go:build cgo

package connector

import (
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

func TestSQLPoolBudgetRealSQLite(t *testing.T) {
	for _, target := range []struct {
		name, database string
		want           int
	}{
		{"memory", ":memory:", 1},
		{"file", filepath.Join(t.TempDir(), "budget.db"), 25},
	} {
		t.Run(target.name, func(t *testing.T) {
			conn, err := (&Sqlite{}).Connect(db.Config{Database: target.database, MaxOpenConnsLimit: target.want})
			if err != nil {
				t.Fatal(err)
			}
			database := db.NewDB(conn)
			defer func() { _ = database.Close() }()
			native, ok := conn.(*db.SQLConnection)
			if !ok || native.DB.Stats().MaxOpenConnections != target.want {
				t.Fatal("database/sql capacity does not match effective pool")
			}
			if _, err := database.Execute("CREATE TABLE budget_check (id INTEGER PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Execute("INSERT INTO budget_check (id) VALUES (?)", 49); err != nil {
				t.Fatal(err)
			}
			rows, err := database.Query("SELECT id FROM budget_check")
			if err != nil || len(rows) != 1 || rows[0]["id"] != int64(49) {
				t.Fatalf("SQLite data=%v error=%v", rows, err)
			}
		})
	}
}
