package db

import (
	"context"
	"errors"
	"testing"
)

// TestResolveTableNameUsesPrefix 验证对外暴露的表名解析方法会自动拼接前缀。
func TestResolveTableNameUsesPrefix(t *testing.T) {
	db := NewDB(&mockConnection{})
	db.prefix = "tg_"

	fullName := db.ResolveTableName("graph_search_history")
	if fullName != "tg_graph_search_history" {
		t.Fatalf("解析后的完整表名错误，期望 tg_graph_search_history，实际为 %s", fullName)
	}
}

// TestResolveTableNameWithoutPrefix 验证无前缀时保持原始表名。
func TestResolveTableNameWithoutPrefix(t *testing.T) {
	db := NewDB(&mockConnection{})

	fullName := db.ResolveTableName("graph_search_history")
	if fullName != "graph_search_history" {
		t.Fatalf("无前缀时表名不应变化，期望 graph_search_history，实际为 %s", fullName)
	}
}

// TestDatabaseRawExecutionUsesContextualAndFallbackDrivers 验证 DB 原生执行优先传递上下文，
// 并兼容只实现传统 RawQueryable 的连接，同时在访问驱动前拒绝非法输入。
func TestDatabaseRawExecutionUsesContextualAndFallbackDrivers(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{ name string }{"trace"}, "db-execute")
	contextual := &modelBusinessConnection{updateCount: 3, rows: []map[string]interface{}{{"value": int64(1)}}}
	database := NewDB(contextual)
	affected, err := database.ExecuteContext(ctx, "UPDATE users SET active = ? WHERE id = ?", true, 1)
	if err != nil || affected != 3 || contextual.lastContext != ctx {
		t.Fatalf("上下文原生执行错误: affected=%d ctx=%v err=%v", affected, contextual.lastContext, err)
	}
	rows, err := database.QueryContext(ctx, "SELECT value FROM users WHERE id = ?", 1)
	if err != nil || len(rows) != 1 || contextual.lastContext != ctx {
		t.Fatalf("上下文原生查询错误: rows=%#v ctx=%v err=%v", rows, contextual.lastContext, err)
	}

	fallback := &queryHardeningConnection{}
	if affected, err := NewDB(fallback).Execute("DELETE FROM users WHERE id = ?", 1); err != nil || affected != 1 {
		t.Fatalf("传统 RawQueryable 执行错误: affected=%d err=%v", affected, err)
	}
	// 使用类型化空上下文验证异常边界，避免与生产代码中的直接 nil 调用混淆。
	var nilContext context.Context
	if _, err := database.ExecuteContext(nilContext, "SELECT 1"); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("nil 执行上下文应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if _, err := database.Execute("UPDATE users SET active = ?", true, false); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("占位符错配应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if _, err := NewDB(&mockConnection{}).Execute("DELETE FROM users"); err == nil {
		t.Fatal("不支持原生执行的连接必须返回错误")
	}
}

// TestDatabaseRawOperationsHaveDefaultTimeout 验证便捷原生 API 不会把无界 Background 直接传给驱动。
func TestDatabaseRawOperationsHaveDefaultTimeout(t *testing.T) {
	connection := &modelBusinessConnection{updateCount: 1, rows: []map[string]interface{}{{"value": int64(1)}}}
	database := NewDB(connection)
	if _, err := database.Query("SELECT value FROM users"); err != nil {
		t.Fatalf("原生查询失败: %v", err)
	}
	if _, ok := connection.lastContext.Deadline(); !ok {
		t.Fatal("便捷原生查询必须携带默认截止时间")
	}
}
