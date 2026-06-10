package db

import (
	"strings"
	"testing"
)

// TestQueryRejectsUnsafeTableName 验证查询构建器不会接受带有注入片段的表名。
func TestQueryRejectsUnsafeTableName(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users; DROP TABLE audit_logs").Select()
	if err == nil {
		t.Fatal("包含注入片段的表名应被拒绝")
	}
}

// TestQueryRejectsUnsafeOrderClause 验证排序子句只能包含受控字段和方向。
func TestQueryRejectsUnsafeOrderClause(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Order("id desc; DROP TABLE users").Select()
	if err == nil {
		t.Fatal("包含注入片段的排序子句应被拒绝")
	}
}

// TestQueryRejectsUnsafeWhereField 验证字段型快捷方法不会把不安全标识符直接拼进 SQL。
func TestQueryRejectsUnsafeWhereFieldName(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").WhereLike("name OR 1=1 --", "%admin%").Select()
	if err == nil {
		t.Fatal("包含注入片段的字段名应被拒绝")
	}
}

// TestQueryRejectsUnsafeInsertField 验证写入数据时的列名同样不能包含注入片段。
func TestQueryRejectsUnsafeInsertField(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Insert(map[string]interface{}{
		"name, is_admin = 1 --": "attacker",
	})
	if err == nil {
		t.Fatal("包含注入片段的写入列名应被拒绝")
	}
}

// ==================== 1.3 Delete/Update 无 WHERE 保护测试 ====================

// TestDeleteWithoutWhereBlocked 验证无 WHERE 条件的 DELETE 被拦截。
func TestDeleteWithoutWhereBlocked(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Delete()
	if err == nil {
		t.Fatal("无 WHERE 条件的 DELETE 应被拦截")
	}
	if !strings.Contains(err.Error(), "禁止无 WHERE") {
		t.Fatalf("错误消息不正确: %s", err.Error())
	}
}

// TestDeleteWithWhereAllowed 验证有 WHERE 条件的 DELETE 正常执行。
func TestDeleteWithWhereAllowed(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Where("id = ?", 1).Delete()
	if err != nil {
		t.Fatalf("有 WHERE 条件的 DELETE 应正常执行，错误: %v", err)
	}
}

// TestUpdateWithoutWhereBlocked 验证无 WHERE 条件的 UPDATE 被拦截。
func TestUpdateWithoutWhereBlocked(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Update(map[string]interface{}{"name": "test"})
	if err == nil {
		t.Fatal("无 WHERE 条件的 UPDATE 应被拦截")
	}
	if !strings.Contains(err.Error(), "禁止无 WHERE") {
		t.Fatalf("错误消息不正确: %s", err.Error())
	}
}

// TestUpdateWithWhereAllowed 验证有 WHERE 条件的 UPDATE 正常执行。
func TestUpdateWithWhereAllowed(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Where("id = ?", 1).Update(map[string]interface{}{"name": "test"})
	if err != nil {
		t.Fatalf("有 WHERE 条件的 UPDATE 应正常执行，错误: %v", err)
	}
}

// ==================== 1.4 WhereField/WhereMap/WhereFields 安全 API 测试 ====================

// TestWhereFieldSafe 验证 WhereField 生成正确的参数化条件。
func TestWhereFieldSafe(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").WhereField("age", ">=", 18).WhereField("status", "=", 1)
	if len(q.where) != 2 {
		t.Fatalf("WhereField 应生成 2 个 where 条件，实际 %d", len(q.where))
	}
	if q.where[0] != "age >= ?" {
		t.Fatalf("WhereField 条件格式不正确: %s", q.where[0])
	}
	if q.where[1] != "status = ?" {
		t.Fatalf("WhereField 条件格式不正确: %s", q.where[1])
	}
	if len(q.args) != 2 {
		t.Fatalf("WhereField args 数量不正确，期望 2，实际 %d", len(q.args))
	}
	if q.args[0] != 18 || q.args[1] != 1 {
		t.Fatalf("WhereField args 值不正确: %v", q.args)
	}
}

// TestWhereFieldRejectsUnsafeField 验证 WhereField 拒绝非法字段名。
func TestWhereFieldRejectsUnsafeField(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").WhereField("name; DROP TABLE users --", "=", "test")
	_, err := q.Select()
	if err == nil {
		t.Fatal("WhereField 应拒绝包含注入片段的字段名")
	}
}

// TestWhereFieldRejectsUnsafeOperator 验证 WhereField 拒绝非法操作符。
func TestWhereFieldRejectsUnsafeOperator(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").WhereField("name", "= 1 OR 1=1 --", "test")
	_, err := q.Select()
	if err == nil {
		t.Fatal("WhereField 应拒绝非法操作符")
	}
}

// TestWhereFieldAllowedOperators 验证所有安全操作符都能通过。
func TestWhereFieldAllowedOperators(t *testing.T) {
	ops := []string{"=", "!=", "<>", ">", ">=", "<", "<=", "LIKE", "like", "NOT LIKE", "not like", "IS", "IS NOT"}
	db := NewDB(&mockConnection{})

	for _, op := range ops {
		q := db.Table("users").WhereField("name", op, "test")
		if q.err != nil {
			t.Fatalf("合法操作符 %q 被拒绝: %v", op, q.err)
		}
	}
}

// TestWhereMapSafe 验证 WhereMap 生成正确的等值参数化条件。
func TestWhereMapSafe(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").WhereMap(map[string]interface{}{
		"status": 1,
	})

	if len(q.where) != 1 {
		t.Fatalf("WhereMap 应生成 1 个 where 条件，实际 %d", len(q.where))
	}
	if q.where[0] != "status = ?" {
		t.Fatalf("WhereMap 条件格式不正确: %s", q.where[0])
	}
}

// TestWhereMapRejectsUnsafeKey 验证 WhereMap 拒绝非法键名。
func TestWhereMapRejectsUnsafeKey(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").WhereMap(map[string]interface{}{
		"name; DROP TABLE users": "attacker",
	})
	_, err := q.Select()
	if err == nil {
		t.Fatal("WhereMap 应拒绝包含注入片段的键名")
	}
}

// TestWhereFieldsSafe 验证 WhereFields 三元组条件正确生成。
func TestWhereFieldsSafe(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").WhereFields([][]interface{}{
		{"age", ">=", 18},
		{"status", "=", 1},
	})

	if len(q.where) != 2 {
		t.Fatalf("WhereFields 应生成 2 个 where 条件，实际 %d", len(q.where))
	}
	if q.where[0] != "age >= ?" {
		t.Fatalf("WhereFields 第一个条件不正确: %s", q.where[0])
	}
	if q.where[1] != "status = ?" {
		t.Fatalf("WhereFields 第二个条件不正确: %s", q.where[1])
	}
}

// TestWhereFieldsRejectsInvalidLength 验证 WhereFields 拒绝非三元组。
func TestWhereFieldsRejectsInvalidLength(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").WhereFields([][]interface{}{
		{"age", ">="}, // 只有两个元素
	})
	_, err := q.Select()
	if err == nil {
		t.Fatal("WhereFields 应拒绝非三元组条件")
	}
}

// TestWhereFieldsRejectsNonStringField 验证 WhereFields 拒绝非 string 类型字段。
func TestWhereFieldsRejectsNonStringField(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").WhereFields([][]interface{}{
		{123, "=", "test"}, // 字段名不是 string
	})
	_, err := q.Select()
	if err == nil {
		t.Fatal("WhereFields 应拒绝非 string 类型的字段名")
	}
}

// TestWhereFieldWithDelete 验证 WhereField 配合 Delete 可以通过 WHERE 检查。
func TestWhereFieldWithDelete(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").WhereField("id", "=", 1).Delete()
	if err != nil {
		t.Fatalf("WhereField + Delete 应正常执行，错误: %v", err)
	}
}

// TestWhereMapWithUpdate 验证 WhereMap 配合 Update 可以通过 WHERE 检查。
func TestWhereMapWithUpdate(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").WhereMap(map[string]interface{}{"id": 1}).Update(map[string]interface{}{"name": "test"})
	if err != nil {
		t.Fatalf("WhereMap + Update 应正常执行，错误: %v", err)
	}
}
