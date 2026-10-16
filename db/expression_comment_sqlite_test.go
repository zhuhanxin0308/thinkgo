//go:build cgo

package db_test

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

func TestWhereExpSQLiteCommentBoundary(t *testing.T) {
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
	if _, err := handle.Exec("CREATE TABLE docs (id INTEGER PRIMARY KEY, deleted_at INTEGER NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec("INSERT INTO docs (id, deleted_at) VALUES (1, NULL), (2, 1), (3, NULL), (4, NULL), (5, NULL)"); err != nil {
		t.Fatal(err)
	}
	assertIDs := func(rows []map[string]interface{}, err error, want ...int64) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]int64, len(rows))
		for index, row := range rows {
			id, ok := row["id"].(int64)
			if !ok {
				t.Fatalf("unexpected id type: %T", row["id"])
			}
			ids[index] = id
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("row identities=%v want=%v", ids, want)
		}
	}
	query := database.Table("docs").Field("id").WhereExp("id", ">", "0").Where("deleted_at IS NULL").Order("id ASC")
	rows, err := query.Select()
	assertIDs(rows, err, 1, 3, 4, 5)
	rows, err = query.Limit(2).Select()
	assertIDs(rows, err, 1, 3)
	// 固定的原生 SQL 负对照证明 SQLite 确实会截断过滤条件和 LIMIT。
	rows, err = database.Query("SELECT id FROM docs WHERE id > 0 -- AND deleted_at IS NULL ORDER BY id ASC LIMIT 2")
	assertIDs(rows, err, 1, 2, 3, 4, 5)
	for _, expression := range []string{"0 -- ", "0--", "0/**/", "0/*1*/", "0*/", "0 #"} {
		for _, limit := range []int{0, 2} {
			t.Run(fmt.Sprintf("reject/%s/limit_%d", expression, limit), func(t *testing.T) {
				rows, err := database.Table("docs").Field("id").WhereExp("id", ">", expression).
					Where("deleted_at IS NULL").Limit(limit).Select()
				if !errors.Is(err, db.ErrInvalidQuery) || len(rows) != 0 {
					t.Fatalf("comment reached execution: expression=%q rows=%v err=%v", expression, rows, err)
				}
			})
		}
	}
	for _, expression := range []string{"-1", "3 - 2", "0 - -1", "0-(-1)", "4 / 2", "2 * 1", "ABS(?)", "-ABS(?)"} {
		t.Run("allow/"+expression, func(t *testing.T) {
			var args []interface{}
			if expression == "ABS(?)" || expression == "-ABS(?)" {
				args = []interface{}{-1}
			}
			rows, err := database.Table("docs").Field("id").WhereExp("id", ">=", expression, args...).
				Where("deleted_at IS NULL").Order("id ASC").Limit(2).Select()
			if expression == "4 / 2" || expression == "2 * 1" {
				assertIDs(rows, err, 3, 4)
			} else {
				assertIDs(rows, err, 1, 3)
			}
		})
	}
}
