package db

import "testing"

// TestWhereRejectsUnsafeRawCondition 验证默认 Where 不再接受拼接出来的原始 SQL。
func TestWhereRejectsUnsafeRawCondition(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Where("name = 'admin' OR 1=1 --").Select()
	if err == nil {
		t.Fatal("默认 Where 应拒绝包含拼接值的原始 SQL 条件")
	}
}

// TestWhereAllowsParameterizedCondition 验证默认 Where 仍然支持参数化条件。
func TestWhereAllowsParameterizedCondition(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Where("name = ?", "admin").Select()
	if err != nil {
		t.Fatalf("参数化 Where 条件应正常通过，实际错误: %v", err)
	}
}

// TestWhereAllowsIdentifierShorthand 验证默认 Where 兼容旧版 field,value 速记写法。
func TestWhereAllowsIdentifierShorthand(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").Where("id", 1)
	if q.err != nil {
		t.Fatalf("field,value 速记写法应被规范化为安全条件，实际错误: %v", q.err)
	}
	if len(q.where) != 1 || q.where[0] != "id = ?" {
		t.Fatalf("速记写法应被转换为 'id = ?'，实际 where=%v", q.where)
	}
}

// TestWhereAllowsNullPredicate 验证默认 Where 支持安全的 NULL 谓词。
func TestWhereAllowsNullPredicate(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Where("delete_time IS NULL").Select()
	if err != nil {
		t.Fatalf("IS NULL 条件应正常通过，实际错误: %v", err)
	}
}

// TestHavingRejectsUnsafeRawCondition 验证默认 Having 不再接受拼接式原始 SQL。
func TestHavingRejectsUnsafeRawCondition(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").Group("status").Having("total > 1; DROP TABLE users")
	if q.err == nil {
		t.Fatal("默认 Having 应拒绝包含注入片段的原始 SQL")
	}
}

// TestHavingAllowsParameterizedCondition 验证默认 Having 支持参数化安全条件。
func TestHavingAllowsParameterizedCondition(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").Group("status").Having("total >= ?", 2)
	if q.err != nil {
		t.Fatalf("参数化 Having 条件应正常通过，实际错误: %v", q.err)
	}
	if q.having != "total >= ?" {
		t.Fatalf("Having 条件格式不正确，实际为 %q", q.having)
	}
}

// TestHavingRawKeepsExplicitRawEntry 验证显式 raw API 仍可保留复杂 HAVING 表达式。
func TestHavingRawKeepsExplicitRawEntry(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").Group("status").HavingRaw("COUNT(id) > ?", 1)
	if q.err != nil {
		t.Fatalf("HavingRaw 应保留显式原始 SQL 入口，实际错误: %v", q.err)
	}
	if q.having != "COUNT(id) > ?" {
		t.Fatalf("HavingRaw 应保留原始条件，实际为 %q", q.having)
	}
}
