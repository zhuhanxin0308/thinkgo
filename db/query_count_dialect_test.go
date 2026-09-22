package db

import (
	"context"
	"strings"
	"testing"
)

// recordingRawConn 记录最后一次执行的原生 SQL，用于断言高级 COUNT 的子查询包裹。
type recordingRawConn struct {
	connectionIdentityState
	lastQuery string
	lastArgs  []interface{}
	countVal  int64
}

func (c *recordingRawConn) Select(context.Context, SelectRequest) ([]map[string]interface{}, error) {
	return []map[string]interface{}{}, nil
}
func (c *recordingRawConn) Insert(_ context.Context, request InsertRequest) (InsertResult, error) {
	return InsertResult{Affected: 1, ID: int64(1), IDKnown: request.WantsID()}, nil
}
func (c *recordingRawConn) Update(context.Context, UpdateRequest) (UpdateResult, error) {
	return UpdateResult{Affected: 1}, nil
}
func (c *recordingRawConn) Delete(context.Context, DeleteRequest) (DeleteResult, error) {
	return DeleteResult{Deleted: 1}, nil
}
func (c *recordingRawConn) Count(context.Context, CountRequest) (int64, error) { return 0, nil }
func (c *recordingRawConn) Close() error                                       { return nil }

func (c *recordingRawConn) Query(sql string, args ...interface{}) ([]map[string]interface{}, error) {
	c.lastQuery = sql
	c.lastArgs = args
	return []map[string]interface{}{{"tg_count": c.countVal}}, nil
}
func (c *recordingRawConn) QueryContext(_ context.Context, sql string, args ...interface{}) ([]map[string]interface{}, error) {
	return c.Query(sql, args...)
}
func (c *recordingRawConn) Execute(sql string, args ...interface{}) (int64, error) {
	c.lastQuery = sql
	c.lastArgs = args
	return 1, nil
}
func (c *recordingRawConn) ExecuteContext(_ context.Context, sql string, args ...interface{}) (int64, error) {
	return c.Execute(sql, args...)
}

// TestCountWithJoinWrapsSubquery 验证带 JOIN 的 Count 会包裹子查询并携带连接与 WHERE 参数。
func TestCountWithJoinWrapsSubquery(t *testing.T) {
	conn := &recordingRawConn{countVal: 7}
	database := NewDB(conn)

	total, err := database.Table("orders").
		Join("users", "orders.user_id = users.id").
		Where("status = ?", 1).
		Count()
	if err != nil {
		t.Fatalf("Count 不应报错: %v", err)
	}
	if total != 7 {
		t.Fatalf("Count 应返回子查询计数 7，实际 %d", total)
	}
	if !strings.HasPrefix(conn.lastQuery, "SELECT COUNT(*) AS tg_count FROM (") {
		t.Fatalf("带 JOIN 的 Count 应包裹子查询，实际 SQL: %s", conn.lastQuery)
	}
	if !strings.Contains(conn.lastQuery, "JOIN users ON orders.user_id = users.id") {
		t.Fatalf("子查询应保留 JOIN，实际 SQL: %s", conn.lastQuery)
	}
	if len(conn.lastArgs) != 1 || conn.lastArgs[0] != 1 {
		t.Fatalf("Count 应携带 WHERE 参数 [1]，实际 %v", conn.lastArgs)
	}
}

// TestCountWithDistinctKeepsFields 验证 DISTINCT 的 Count 子查询保留字段以正确去重。
func TestCountWithDistinctKeepsFields(t *testing.T) {
	conn := &recordingRawConn{countVal: 3}
	database := NewDB(conn)

	total, err := database.Table("orders").Distinct().Field("user_id").Count()
	if err != nil {
		t.Fatalf("Count 不应报错: %v", err)
	}
	if total != 3 {
		t.Fatalf("DISTINCT Count 应返回 3，实际 %d", total)
	}
	if !strings.Contains(conn.lastQuery, "SELECT DISTINCT user_id FROM") {
		t.Fatalf("DISTINCT 子查询应保留字段，实际 SQL: %s", conn.lastQuery)
	}
}

// TestSimpleCountDoesNotWrap 验证无 JOIN/GROUP 的简单 Count 仍走快速路径（不包裹子查询）。
func TestSimpleCountDoesNotWrap(t *testing.T) {
	conn := &recordingRawConn{countVal: 99}
	database := NewDB(conn)

	// 简单 Count 走 connection.Count（recordingRawConn.Count 返回 0），不应触碰 Query。
	_, err := database.Table("orders").Where("status = ?", 1).Count()
	if err != nil {
		t.Fatalf("Count 不应报错: %v", err)
	}
	if conn.lastQuery != "" {
		t.Fatalf("简单 Count 不应走原生子查询路径，却执行了: %s", conn.lastQuery)
	}
}
