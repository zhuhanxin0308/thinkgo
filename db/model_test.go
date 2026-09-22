package db

import (
	"testing"
)

// TestSoftDeleteConfig 验证软删除配置
func TestSoftDeleteConfig(t *testing.T) {
	db := NewDB(&mockConnection{})

	// 默认删除字段名
	m := NewModel(db, "users").SoftDelete()
	if !m.softDelete {
		t.Fatal("SoftDelete() 应启用软删除")
	}
	if m.deleteTimeField != "delete_time" {
		t.Fatalf("默认删除字段应为 delete_time，实际 %s", m.deleteTimeField)
	}

	// 自定义删除字段名
	m2 := NewModel(db, "users").SoftDelete("deleted_at")
	if m2.deleteTimeField != "deleted_at" {
		t.Fatalf("自定义删除字段应为 deleted_at，实际 %s", m2.deleteTimeField)
	}
}

// TestSoftDeleteQueryFilter 验证 query() 自动添加软删除过滤
func TestSoftDeleteQueryFilter(t *testing.T) {
	db := NewDB(&mockConnection{})
	m := NewModel(db, "users").SoftDelete()

	// 默认查询应自动添加 IS NULL 过滤
	q := m.query()
	if len(q.where) != 1 || q.where[0] != "delete_time IS NULL" {
		t.Fatalf("默认查询应自动添加 delete_time IS NULL，实际 %v", q.where)
	}
}

// TestSoftDeleteWithTrashed 验证 WithTrashed 不添加过滤
func TestSoftDeleteWithTrashed(t *testing.T) {
	db := NewDB(&mockConnection{})
	m := NewModel(db, "users").SoftDelete()

	q := m.WithTrashed().PrepareQuery()
	if len(q.where) != 0 {
		t.Fatalf("WithTrashed() 查询不应有软删除过滤条件，实际 %v", q.where)
	}
}

// TestSoftDeleteOnlyTrashed 验证 OnlyTrashed 只查已删除
func TestSoftDeleteOnlyTrashed(t *testing.T) {
	db := NewDB(&mockConnection{})
	m := NewModel(db, "users").SoftDelete()

	q := m.OnlyTrashed().PrepareQuery()
	if len(q.where) != 1 || q.where[0] != "delete_time IS NOT NULL" {
		t.Fatalf("OnlyTrashed() 应添加 IS NOT NULL 条件，实际 %v", q.where)
	}
}

// TestSoftDeleteFlagReset 验证一次性标记重置
func TestSoftDeleteFlagReset(t *testing.T) {
	db := NewDB(&mockConnection{})
	m := NewModel(db, "users").SoftDelete()

	// 第一次 WithTrashed
	_ = m.WithTrashed().PrepareQuery()

	// 第二次应恢复默认（有 IS NULL 过滤）
	q2 := m.query()
	if len(q2.where) != 1 || q2.where[0] != "delete_time IS NULL" {
		t.Fatalf("WithTrashed 标记应重置，第二次查询应有软删除过滤，实际 %v", q2.where)
	}
}

// TestModelTableName 验证表名推断
func TestModelTableName(t *testing.T) {
	type User struct{}
	type UserSession struct{}

	name1, err := GetTableName(User{})
	if err != nil {
		t.Fatalf("推断 User 表名失败: %v", err)
	}
	if name1 != "user" {
		t.Fatalf("User 应推断为 user，实际 %s", name1)
	}

	name2, err := GetTableName(UserSession{})
	if err != nil {
		t.Fatalf("推断 UserSession 表名失败: %v", err)
	}
	if name2 != "user_session" {
		t.Fatalf("UserSession 应推断为 user_session，实际 %s", name2)
	}
}

// TestToSnakeCase 验证驼峰转下划线
func TestToSnakeCase(t *testing.T) {
	tests := []struct{ input, expected string }{
		{"ID", "id"},
		{"UserName", "user_name"},
		{"HTMLParser", "html_parser"},
		{"UserID", "user_id"},
		{"Simple", "simple"},
		{"", ""},
	}
	for _, tt := range tests {
		result := ToSnakeCase(tt.input)
		if result != tt.expected {
			t.Fatalf("ToSnakeCase(%s) = %s，期望 %s", tt.input, result, tt.expected)
		}
	}
}
