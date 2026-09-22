package db

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateIdentifierPreservesPartRules 验证标识符快速校验仍遵守分段和字符边界。
func TestValidateIdentifierPreservesPartRules(t *testing.T) {
	valid := []string{"id", "_private", "users.user_id", "a1.b2"}
	for _, value := range valid {
		if err := validateIdentifier(value); err != nil {
			t.Fatalf("合法标识符被拒绝: value=%q err=%v", value, err)
		}
	}
	longPart := strings.Repeat("a", maxIdentifierPartLength+1)
	invalid := []string{"", "1id", "users..id", ".id", "id.", "user-name", "user name", longPart}
	for _, value := range invalid {
		if err := validateIdentifier(value); err == nil {
			t.Fatalf("非法标识符未被拒绝: value=%q", value)
		}
	}
}

// TestNormalizeOperatorPreservesCanonicalForms 验证操作符大小写和多余空白仍按原契约规范化。
func TestNormalizeOperatorPreservesCanonicalForms(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{input: "=", want: "="},
		{input: "LIKE", want: "LIKE"},
		{input: " not   like ", want: "NOT LIKE"},
		{input: " is   not ", want: "IS NOT"},
	}
	for _, testCase := range cases {
		got, err := normalizeOperator(testCase.input)
		if err != nil || got != testCase.want {
			t.Fatalf("操作符规范化错误: input=%q got=%q want=%q err=%v", testCase.input, got, testCase.want, err)
		}
	}
}

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

// TestQueryRejectsEmptyInsertData 验证空插入数据会被拒绝，
// 避免生成 INSERT INTO table () VALUES () 这类跨方言不可靠 SQL。
func TestQueryRejectsEmptyInsertData(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Insert(map[string]interface{}{})
	if err == nil {
		t.Fatal("空插入数据应返回错误")
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
	if !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("错误类型不正确: %v", err)
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
	if !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("错误类型不正确: %v", err)
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

// TestUpdateRejectsEmptyDataWithoutSetExpression 验证普通 UPDATE 不接受空数据，
// 但 Inc/Dec 这类显式 SET 表达式由单独路径负责。
func TestUpdateRejectsEmptyDataWithoutSetExpression(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Where("id = ?", 1).Update(map[string]interface{}{})
	if err == nil {
		t.Fatal("无 SET 字段且无 Inc/Dec 表达式的 UPDATE 应返回错误")
	}
}

// TestUpdateAllowsOnlySetExpression 验证 Inc/Dec 可配合空 map 使用，
// 这是文档承诺的自增自减写法，不能被空数据校验误伤。
func TestUpdateAllowsOnlySetExpression(t *testing.T) {
	conn := &batchRecorderConn{}
	db := NewDB(conn)

	_, err := db.Table("users").Where("id = ?", 1).Inc("score", 1).Update(map[string]interface{}{})
	if err != nil {
		t.Fatalf("仅包含 Inc/Dec 表达式的 UPDATE 应正常执行，实际错误: %v", err)
	}
	if len(conn.execSQL) != 1 || !strings.Contains(conn.execSQL[0], "score = score + ?") {
		t.Fatalf("自增表达式未写入 UPDATE SQL，实际 SQL: %#v", conn.execSQL)
	}
	if len(conn.execArgs) != 1 || len(conn.execArgs[0]) != 2 || conn.execArgs[0][0] != 1 || conn.execArgs[0][1] != 1 {
		t.Fatalf("自增步长和 WHERE 值应按顺序绑定，实际参数: %#v", conn.execArgs)
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
