package db

import (
	"strings"
	"testing"
)

// TestJoinAppliesPrefixForName 验证通过 Name() 创建的查询，JOIN 表会套用与主表一致的前缀。
func TestJoinAppliesPrefixForName(t *testing.T) {
	db := NewDB(&mockConnection{})
	db.prefix = "tg_"

	sqlStr, _, err := db.Name("user").
		Join("profile", "user.id = profile.user_id").
		BuildSelectSQL()
	if err != nil {
		t.Fatalf("构建 JOIN SQL 失败: %v", err)
	}

	if !strings.Contains(sqlStr, "FROM tg_user") {
		t.Fatalf("主表应带前缀 tg_user，实际 SQL: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, "JOIN tg_profile ON") {
		t.Fatalf("JOIN 表应套用与主表一致的前缀 tg_profile，实际 SQL: %s", sqlStr)
	}
}

// TestJoinKeepsRawTableForTable 验证通过 Table() 创建的查询（完整表名）不会给 JOIN 表追加前缀。
func TestJoinKeepsRawTableForTable(t *testing.T) {
	db := NewDB(&mockConnection{})
	db.prefix = "tg_"

	sqlStr, _, err := db.Table("tg_user").
		Join("tg_profile", "tg_user.id = tg_profile.user_id").
		BuildSelectSQL()
	if err != nil {
		t.Fatalf("构建 JOIN SQL 失败: %v", err)
	}

	if strings.Contains(sqlStr, "tg_tg_profile") {
		t.Fatalf("Table() 模式不应给 JOIN 表重复追加前缀，实际 SQL: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, "JOIN tg_profile ON") {
		t.Fatalf("JOIN 表名不正确，实际 SQL: %s", sqlStr)
	}
}

// TestJoinRejectsInjection 验证 JOIN 表名与条件中的注入片段仍会被拒绝。
func TestJoinRejectsInjection(t *testing.T) {
	db := NewDB(&mockConnection{})

	if q := db.Name("user").Join("profile; DROP TABLE users", "user.id = profile.uid"); q.err == nil {
		t.Fatal("JOIN 表名包含注入片段应被拒绝")
	}
	if q := db.Name("user").Join("profile", "user.id = profile.uid OR 1=1"); q.err == nil {
		t.Fatal("JOIN 条件包含注入片段应被拒绝")
	}
}
