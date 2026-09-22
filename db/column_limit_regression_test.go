package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/db/builder"
)

type columnLimitDriver struct {
	count  int
	closed atomic.Int32
}

func (fixture *columnLimitDriver) Open(string) (driver.Conn, error) {
	return &columnLimitConnection{fixture: fixture}, nil
}

type columnLimitConnection struct {
	fixture *columnLimitDriver
}

func (*columnLimitConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("测试驱动不支持预编译")
}
func (*columnLimitConnection) Close() error { return nil }
func (*columnLimitConnection) Begin() (driver.Tx, error) {
	return &hardeningSQLDriverTransaction{}, nil
}
func (connection *columnLimitConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &columnLimitRows{fixture: connection.fixture}, nil
}

type columnLimitRows struct {
	fixture *columnLimitDriver
	index   int
}

func (*columnLimitRows) Columns() []string { return []string{"id", "value"} }
func (rows *columnLimitRows) Close() error { rows.fixture.closed.Add(1); return nil }
func (rows *columnLimitRows) Next(values []driver.Value) error {
	if rows.index == rows.fixture.count {
		return io.EOF
	}
	rows.index++
	values[0], values[1] = int64(rows.index), int64(rows.index)
	return nil
}

// TestColumnResultLimitsAcrossExecutionPaths 验证普通、事务及高级查询都限制物化结果，并关闭行游标。
func TestColumnResultLimitsAcrossExecutionPaths(t *testing.T) {
	for _, count := range []int{maxQueryResultRows - 1, maxQueryResultRows, maxQueryResultRows + 1} {
		for _, mode := range []string{"普通", "事务", "高级"} {
			for _, keyed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/字典=%t", mode, count, keyed), func(t *testing.T) {
					fixture := &columnLimitDriver{count: count}
					name := fmt.Sprintf("column-limit-%d", hardeningSQLDriverSequence.Add(1))
					sql.Register(name, fixture)
					handle, err := sql.Open(name, "")
					if err != nil {
						t.Fatal(err)
					}
					database := NewDB(&SQLConnection{DB: handle, Builder: &builder.Sqlite{}})
					t.Cleanup(func() { _ = database.Close() })
					query := database.Table("records")
					if mode == "事务" {
						transaction, err := database.BeginTx(t.Context(), nil)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = transaction.Rollback() })
						query = transaction.Table("records")
					} else if mode == "高级" {
						query = query.Distinct()
					}
					var result interface{}
					if keyed {
						result, err = query.Column("value", "id")
					} else {
						result, err = query.Column("value")
					}
					if count > maxQueryResultRows {
						if !errors.Is(err, ErrQueryResultTooMany) {
							t.Fatalf("超限结果未拒绝: %v", err)
						}
					} else {
						if err != nil {
							t.Fatal(err)
						}
						length := 0
						if keyed {
							length = len(result.(map[string]interface{}))
						} else {
							length = len(result.([]interface{}))
						}
						if length != count {
							t.Fatalf("合法结果被截断: %d", length)
						}
					}
					if fixture.closed.Load() != 1 {
						t.Fatalf("行游标未恰好关闭一次: %d", fixture.closed.Load())
					}
				})
			}
		}
	}
}
