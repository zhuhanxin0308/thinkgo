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
