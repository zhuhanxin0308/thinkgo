package db

import (
	"errors"
	"slices"
	"testing"
)

func TestQualifiedSQLCursorUsesProjectedColumnAcrossRoutes(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	if _, err := raw.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
		CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER, note TEXT);
		INSERT INTO users VALUES (1, 'first'), (2, 'second'), (3, 'third');
		INSERT INTO orders VALUES (101, 1, 'one'), (102, 2, 'two'), (103, 3, 'three');`); err != nil {
		t.Fatal(err)
	}
	joined := database.Table("users").Join("orders", "orders.user_id = users.id")
	for _, selection := range []struct{ fields, key string }{
		{"users.id, orders.note", "id"},
		{"users.id AS cursor_id, orders.note", "cursor_id"},
	} {
		t.Run(selection.key, func(t *testing.T) {
			query := joined.Field(selection.fields)
			first, err := query.SeekPage(2, "users.id", nil)
			if err != nil || !first.HasMore || first.NextCursor != int64(2) || first.List[0][selection.key] != int64(1) {
				t.Fatalf("限定名应使用所选结果列读取游标: %+v, %v", first, err)
			}
			next, err := query.SeekPage(2, "users.id", first.NextCursor)
			if err != nil || next.HasMore || len(next.List) != 1 || next.List[0][selection.key] != int64(3) {
				t.Fatalf("后续页必须用相同的限定列继续: %+v, %v", next, err)
			}
			var keys []int64
			if err := query.ChunkById(2, "users.id", func(rows []map[string]any) bool {
				for _, row := range rows {
					keys = append(keys, row[selection.key].(int64))
				}
				return true
			}); err != nil || !slices.Equal(keys, []int64{1, 2, 3}) {
				t.Fatalf("分批游标必须按同一结果列推进: %v, %v", keys, err)
			}
		})
	}
	if _, err := joined.Field("orders.id").SeekPage(1, "users.id", nil); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("不能将另一张表的同名列当作游标: %v", err)
	}
	for _, query := range []*Query{database.Table("users"), database.Table("users").Field("id")} {
		page, err := query.SeekPage(1, "users.id", nil)
		if err != nil || page.NextCursor != int64(1) {
			t.Fatalf("单表默认或未限定投影应读取正确游标: %+v, %v", page, err)
		}
	}
	type userProjection struct{ Name string }
	model := NewModel(database, "users")
	typed, err := SeekPageWithCodec[userProjection](model.Field("users.id, users.name"), 1, "users.id", OrderedCursorCodec{}, nil)
	if err != nil || !typed.HasMore || typed.NextCursor != int64(1) || len(typed.List) != 1 || typed.List[0].Name != "first" {
		t.Fatalf("类型化游标必须读取数据库原始限定列: %+v, %v", typed, err)
	}
}

func TestQualifiedCursorKeepsExactFieldForNonSQLDrivers(t *testing.T) {
	connection := &queryHardeningConnection{selectRows: []map[string]any{
		{"id": int64(11), "address.id": int64(2)},
		{"id": int64(12), "address.id": int64(3)},
	}}
	query := NewDB(connection).Table("records")
	result, err := query.SeekPage(1, "address.id", nil)
	if err != nil || result.NextCursor != int64(2) {
		t.Fatalf("非 SQL 结果应保留原始游标字段: %+v, %v", result, err)
	}
	if _, err := query.SeekPage(1, "unrelated.id", nil); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("非 SQL 结果不得回退到同名叶子字段: %v", err)
	}
}
