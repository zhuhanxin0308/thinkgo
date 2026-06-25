package db

import "testing"

// TestWhereExpRejectsParenKeywordBypass 验证表达式校验不再能被“去空格 + 括号”写法绕过。
// 修复前 (1)OR(1=1)、(0)UNION(SELECT(...)) 这类 payload 会被误判为安全。
func TestWhereExpRejectsParenKeywordBypass(t *testing.T) {
	maliciousExpressions := []string{
		"(1)OR(1=1)",
		"(0)UNION(SELECT(password)FROM(users))",
		"1)OR(1=1",
		"''OR''=''",
		"(CASE(WHEN(1=1)THEN(1)ELSE(0)END))",
		"SLEEP(5)",
		"(SELECT(1))",
		"1;DROP TABLE users",
		"col/**/OR/**/1=1",
	}

	for _, expr := range maliciousExpressions {
		db := NewDB(&mockConnection{})
		q := db.Table("users").WhereExp("id", "=", expr)
		if q.err == nil {
			t.Fatalf("恶意表达式 %q 应被拒绝，但通过了校验（生成 where=%v）", expr, q.where)
		}
	}
}

// TestWhereExpAllowsLegitExpressions 验证合法的列/函数/算术表达式仍可正常使用。
func TestWhereExpAllowsLegitExpressions(t *testing.T) {
	legitExpressions := []string{
		"NOW()",
		"level * 2",
		"score + 10",
		"a.created_at",
		"COUNT(id)",
		"COALESCE(score, 0)",
		"updated_at",
	}

	for _, expr := range legitExpressions {
		db := NewDB(&mockConnection{})
		q := db.Table("users").WhereExp("updated_at", ">", expr)
		if q.err != nil {
			t.Fatalf("合法表达式 %q 不应被拒绝，实际错误: %v", expr, q.err)
		}
	}
}

// TestMongoBuildFilterRejectsOperatorInjection 验证 Mongo 条件值若为 map（典型来自 JSON 请求体）
// 会被拒绝，防止退化为 $ne/$gt 等运算符注入。
func TestMongoBuildFilterRejectsOperatorInjection(t *testing.T) {
	c := &MongoConnection{}

	// 标量值正常。
	if _, err := c.buildFilter([]string{"username = ?"}, []interface{}{"alice"}); err != nil {
		t.Fatalf("标量条件值不应报错: %v", err)
	}

	// map 值（运算符注入）必须被拒绝。
	injection := map[string]interface{}{"$ne": nil}
	if _, err := c.buildFilter([]string{"username = ?"}, []interface{}{injection}); err == nil {
		t.Fatal("map 类型条件值应被拒绝以防 NoSQL 运算符注入")
	}
}
