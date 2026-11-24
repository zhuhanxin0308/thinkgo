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
	for _, query := range []*Query{joined, joined.Field(" * ")} {
		if _, err := query.SeekPage(1, "orders.id", nil); !errors.Is(err, ErrInvalidDatabaseRow) {
			t.Fatalf("默认 JOIN 投影不能把主表主键当作关联表游标: %v", err)
		}
		if err := query.ChunkById(1, "orders.id", func([]map[string]any) bool {
			t.Error("缺少游标的分批查询不能调用业务回调")
			return false
		}); !errors.Is(err, ErrInvalidDatabaseRow) {
			t.Fatalf("分批查询必须拒绝缺少的关联表游标: %v", err)
		}
		page, err := query.SeekPage(1, "users.id", nil)
		if err != nil || page.NextCursor != int64(1) {
			t.Fatalf("默认 JOIN 投影仍应读取主表的游标: %+v, %v", page, err)
		}
	}
	page, err := joined.Field("users.id, orders.id AS order_cursor").SeekPage(1, "orders.id", nil)
	if err != nil || page.NextCursor != int64(101) {
		t.Fatalf("显式投影应读取关联表游标: %+v, %v", page, err)
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

func TestCursorProjectionCannotShadowSourceOrder(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	if _, err := raw.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, score INTEGER);
		INSERT INTO items VALUES (1,100), (2,10), (3,20), (4,30)`); err != nil {
		t.Fatal(err)
	}
	want := []int64{1, 2, 3, 4}
	const fields = "id AS item_id, score AS id"
	check := func(t *testing.T, got []int64, err error) {
		t.Helper()
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("真实主键应按升序各出现一次：got=%v err=%v", got, err)
		}
	}
	type item struct {
		Key int64 `thinkgo:"item_id"`
	}
	model := NewModel(database, "items").Field(fields)
	query := database.Table("items").Field(fields)
	t.Run("SeekPage", func(t *testing.T) {
		var got []int64
		var after any
		for {
			page, err := query.SeekPage(2, "id", after)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range page.List {
				got = append(got, row["item_id"].(int64))
			}
			if !page.HasMore {
				break
			}
			after = page.NextCursor
		}
		check(t, got, nil)
	})
	t.Run("ChunkById", func(t *testing.T) {
		var got []int64
		err := query.ChunkById(2, "id", func(rows []map[string]any) bool {
			for _, row := range rows {
				got = append(got, row["item_id"].(int64))
			}
			return true
		})
		check(t, got, err)
	})
	t.Run("类型化分页", func(t *testing.T) {
		var got []int64
		var after any
		for {
			page, err := SeekPage[item](model, 2, after)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range page.List {
				got = append(got, row.Key)
			}
			if !page.HasMore {
				break
			}
			after = page.NextCursor
		}
		check(t, got, nil)
	})
	t.Run("类型化分批", func(t *testing.T) {
		var got []int64
		err := ChunkById[item](model, 2, func(rows []item) (bool, error) {
			for _, row := range rows {
				got = append(got, row.Key)
			}
			return true, nil
		})
		check(t, got, err)
	})
}

func TestCursorSourceOrderWithCaseCollisionsAndJoins(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	if _, err := raw.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, score INTEGER);
		CREATE TABLE labels (item INTEGER PRIMARY KEY, text TEXT);
		INSERT INTO items VALUES (1,100), (2,10), (3,20), (4,30);
		INSERT INTO labels VALUES (1,'a'), (2,'b'), (3,'c'), (4,'d')`); err != nil {
		t.Fatal(err)
	}
	table := database.Table("items")
	joined := table.Join("labels", "labels.item = items.id")
	for _, order := range []string{"score ASC", "score DESC"} {
		for _, query := range []*Query{
			table.Field("score AS ITEM_ID, id AS item_id, score AS id"),
			joined.Field("items.score AS ITEM_ID, items.id AS item_id, items.score AS id"),
		} {
			page, err := query.Order(order).SeekPage(2, "id", nil)
			if err != nil || len(page.List) != 2 || page.List[0]["item_id"] != int64(1) || page.NextCursor != int64(2) {
				t.Fatalf("排序必须绑定真实源列：%s %+v %v", order, page, err)
			}
		}
	}
	// JOIN 的裸列无法证明属于哪张表；冲突投影必须在回调前拒绝。
	ambiguous := joined.Field("id AS item_id, items.score AS id")
	if err := ambiguous.ChunkById(2, "id", func([]map[string]any) bool {
		t.Fatal("来源不明的游标不能交给业务")
		return false
	}); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatal(err)
	}
	if _, err := table.Field("id AS item_id, score AS item_id").SeekPage(2, "id", nil); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("重复结果名应拒绝：%v", err)
	}
	if _, err := database.Table("items AS i").Field("i.id").SeekPage(2, "i.id", nil); err == nil {
		t.Fatal("表别名应沿用结构化查询的现有校验")
	}
}
