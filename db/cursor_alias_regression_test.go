package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

var cursorProjectionDriverSeq atomic.Uint64

type cursorProjectionSQLDriver struct {
	handler func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error)
}

func (d *cursorProjectionSQLDriver) Open(string) (driver.Conn, error) {
	return &cursorProjectionSQLConn{driver: d}, nil
}

type cursorProjectionSQLConn struct {
	driver *cursorProjectionSQLDriver
}

func (c *cursorProjectionSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}

func (c *cursorProjectionSQLConn) Close() error { return nil }
func (c *cursorProjectionSQLConn) Begin() (driver.Tx, error) {
	return nil, errors.New("not implemented")
}

func (c *cursorProjectionSQLConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	columns, rows, err := c.driver.handler(query, args)
	if err != nil {
		return nil, err
	}
	return &cursorProjectionSQLRows{columns: columns, rows: rows}, nil
}

type cursorProjectionSQLRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *cursorProjectionSQLRows) Columns() []string { return r.columns }
func (r *cursorProjectionSQLRows) Close() error      { return nil }
func (r *cursorProjectionSQLRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}

func newCursorProjectionTestDB(t *testing.T, build Builder, handler func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error)) *DB {
	t.Helper()
	name := fmt.Sprintf("thinkgo_cursor_proj_%d", cursorProjectionDriverSeq.Add(1))
	sql.Register(name, &cursorProjectionSQLDriver{handler: handler})
	rawDB, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open test sql db: %v", err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })
	conn := &SQLConnection{DB: rawDB, Builder: build}
	return NewDB(conn)
}

// TestUnqualifiedSQLCursorResolvesAliasAndRejectsHijack 验证未加表前缀的主键游标在 SQL 查询中
// 正确追踪 AS 列别名，并拒绝将非主键列的 AS id 冒充为游标 (#590)。
func TestUnqualifiedSQLCursorResolvesAliasAndRejectsHijack(t *testing.T) {
	type itemRow struct {
		id    int64
		score int64
		title string
	}
	dataset := []itemRow{
		{id: 1, score: 100, title: "a"},
		{id: 2, score: 50, title: "b"},
		{id: 3, score: 10, title: "c"},
	}

	var executedQueries []string
	database := newCursorProjectionTestDB(t, &builder.Sqlite{}, func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error) {
		executedQueries = append(executedQueries, query)
		var after int64
		if len(args) == 1 {
			after = args[0].Value.(int64)
		}
		var rows [][]driver.Value
		for _, item := range dataset {
			if item.id <= after {
				continue
			}
			rows = append(rows, []driver.Value{item.id, item.title})
			if len(rows) == 3 {
				break
			}
		}
		return []string{"item_id", "title"}, rows, nil
	})
	database.prefix = "tp_"

	for _, pkArg := range []string{"id", ""} {
		for _, fieldExpr := range []string{"id AS item_id, title", "tp_items.id AS item_id, title"} {
			query := database.Name("items").Field(fieldExpr)
			first, err := query.SeekPage(2, pkArg, nil)
			if err != nil {
				t.Fatalf("pk=%q field=%q 首页游标分页失败: %v", pkArg, fieldExpr, err)
			}
			if !first.HasMore || first.NextCursor != int64(2) || len(first.List) != 2 || first.List[0]["item_id"] != int64(1) {
				t.Fatalf("pk=%q field=%q 首页游标结果异常: %+v", pkArg, fieldExpr, first)
			}
			second, err := query.SeekPage(2, pkArg, first.NextCursor)
			if err != nil {
				t.Fatalf("pk=%q field=%q 次页游标分页失败: %v", pkArg, fieldExpr, err)
			}
			if second.HasMore || second.NextCursor != nil || len(second.List) != 1 || second.List[0]["item_id"] != int64(3) {
				t.Fatalf("pk=%q field=%q 次页游标结果异常: %+v", pkArg, fieldExpr, second)
			}

			var visited []int64
			if err := query.ChunkById(2, pkArg, func(rows []map[string]any) bool {
				for _, row := range rows {
					visited = append(visited, row["item_id"].(int64))
				}
				return true
			}); err != nil || !slices.Equal(visited, []int64{1, 2, 3}) {
				t.Fatalf("pk=%q field=%q ChunkById 遍历异常: visited=%v err=%v", pkArg, fieldExpr, visited, err)
			}
		}
	}

	wildcardHijack := database.Name("items")
	wildcardHijack.fields = "tp_items.*, score AS id"

	beforeCount := len(executedQueries)
	for _, invalidQuery := range []*Query{
		database.Name("items").Field("score AS id, title"),
		wildcardHijack,
		database.Name("items").Field("title"),
		database.Name("items").Join("orders", "tp_orders.item_id = tp_items.id").Field("tp_orders.id, tp_orders.title"),
		database.Name("items").Join("orders", "tp_orders.item_id = tp_items.id").Field("tp_orders.id AS id, tp_orders.title"),
	} {
		if _, err := invalidQuery.SeekPage(2, "id", nil); !errors.Is(err, ErrInvalidDatabaseRow) || !strings.Contains(err.Error(), "投影未包含游标字段") {
			t.Fatalf("伪造或缺失游标投影应在前置阶段报错，实际 err=%v", err)
		}
		if err := invalidQuery.ChunkById(2, "id", func([]map[string]any) bool {
			t.Fatal("非法投影不应触发业务回调")
			return false
		}); !errors.Is(err, ErrInvalidDatabaseRow) {
			t.Fatalf("ChunkById 应拒绝伪造游标投影，实际 err=%v", err)
		}
	}
	if len(executedQueries) != beforeCount {
		t.Fatalf("前置投影校验失败不应下发 SQL，多执行了 %d 条", len(executedQueries)-beforeCount)
	}
}
