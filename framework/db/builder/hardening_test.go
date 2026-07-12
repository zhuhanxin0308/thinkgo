package builder

import (
	"reflect"
	"strings"
	"testing"
)

type deterministicWriteBuilder interface {
	Insert(string, map[string]interface{}) (string, []interface{})
	Update(string, map[string]interface{}, []string) (string, []interface{})
}

// TestWriteBuildersSortMapFields 验证所有方言生成稳定 SQL 和参数顺序。
func TestWriteBuildersSortMapFields(t *testing.T) {
	builders := []struct {
		name  string
		build deterministicWriteBuilder
	}{
		{name: "mysql", build: &Mysql{}},
		{name: "pgsql", build: &Pgsql{}},
		{name: "sqlite", build: &Sqlite{}},
		{name: "sqlsrv", build: &Sqlsrv{}},
	}
	data := map[string]interface{}{"zeta": 3, "alpha": 1, "middle": 2}
	for _, testCase := range builders {
		t.Run(testCase.name, func(t *testing.T) {
			insertSQL, insertArgs := testCase.build.Insert("users", data)
			if !orderedFragments(insertSQL, "alpha", "middle", "zeta") {
				t.Fatalf("INSERT 字段顺序不稳定: %q", insertSQL)
			}
			if !reflect.DeepEqual(insertArgs, []interface{}{1, 2, 3}) {
				t.Fatalf("INSERT 参数顺序错误: %#v", insertArgs)
			}
			updateSQL, updateArgs := testCase.build.Update("users", data, []string{"id = ?"})
			if !orderedFragments(updateSQL, "alpha", "middle", "zeta") {
				t.Fatalf("UPDATE 字段顺序不稳定: %q", updateSQL)
			}
			if !reflect.DeepEqual(updateArgs, []interface{}{1, 2, 3}) {
				t.Fatalf("UPDATE 参数顺序错误: %#v", updateArgs)
			}
		})
	}
}

func orderedFragments(text string, fragments ...string) bool {
	last := -1
	for _, fragment := range fragments {
		index := strings.Index(text, fragment)
		if index <= last {
			return false
		}
		last = index
	}
	return true
}

// TestNumberedRebindIgnoresQuotedAndCommentedQuestionMarks 验证只重绑定真实参数。
func TestNumberedRebindIgnoresQuotedAndCommentedQuestionMarks(t *testing.T) {
	query := "SELECT '?' AS literal, value FROM t WHERE id = ? /* ? */ AND state = ? -- ?\n"
	if got := (&Pgsql{}).Rebind(query); got != "SELECT '?' AS literal, value FROM t WHERE id = $1 /* ? */ AND state = $2 -- ?\n" {
		t.Fatalf("PostgreSQL Rebind 误改字面量或注释: %q", got)
	}
	if got := (&Sqlsrv{}).Rebind(query); got != "SELECT '?' AS literal, value FROM t WHERE id = @p1 /* ? */ AND state = @p2 -- ?\n" {
		t.Fatalf("SQL Server Rebind 误改字面量或注释: %q", got)
	}
}

// TestPgsqlRebindPreservesJSONBQuestionOperators 验证 PostgreSQL JSONB 操作符
// 不占用绑定参数序号，双问号转义会还原为单字符存在操作符。
func TestPgsqlRebindPreservesJSONBQuestionOperators(t *testing.T) {
	query := "SELECT payload FROM documents WHERE payload ?? ? AND tags ?| ? AND required ?& ? AND payload @? ? AND tenant_id = ?"
	want := "SELECT payload FROM documents WHERE payload ? $1 AND tags ?| $2 AND required ?& $3 AND payload @? $4 AND tenant_id = $5"
	if got := (&Pgsql{}).Rebind(query); got != want {
		t.Fatalf("PostgreSQL JSONB 操作符重绑定错误:\n got %q\nwant %q", got, want)
	}
}

// TestQuoteIdentifierEscapesClosingDelimiter 验证恶意分隔符只能成为标识符内容。
func TestQuoteIdentifierEscapesClosingDelimiter(t *testing.T) {
	if got := (&Mysql{}).QuoteIdentifier("user`name"); got != "`user``name`" {
		t.Fatalf("MySQL 标识符转义错误: %q", got)
	}
	if got := (&Pgsql{}).QuoteIdentifier(`user"name`); got != `"user""name"` {
		t.Fatalf("PostgreSQL 标识符转义错误: %q", got)
	}
	if got := (&Sqlsrv{}).QuoteIdentifier("user]name"); got != "[user]]name]" {
		t.Fatalf("SQL Server 标识符转义错误: %q", got)
	}
}

// TestFieldAndOrderQuotingHandlesAliasesAndAggregates 验证保留字字段、别名和聚合参数都被引用。
func TestFieldAndOrderQuotingHandlesAliasesAndAggregates(t *testing.T) {
	query := (&Pgsql{}).Select("order", "select AS result, SUM(score) AS total", nil, "select DESC", 10, 0)
	want := `SELECT "select" AS "result", SUM("score") AS "total" FROM "order" ORDER BY "select" DESC LIMIT 10`
	if query != want {
		t.Fatalf("字段或排序引用错误:\n got %q\nwant %q", query, want)
	}
}

// TestQuoteFieldsPreservesQualifiedWildcard 验证 JOIN 默认投影可以安全引用主表，
// 同时保留 SQL 通配符语义，避免把星号错误引用成名为 "*" 的列。
func TestQuoteFieldsPreservesQualifiedWildcard(t *testing.T) {
	cases := []struct {
		name  string
		build interface{ QuoteFields(string) string }
		want  string
	}{
		{name: "mysql", build: &Mysql{}, want: "`users`.*"},
		{name: "pgsql", build: &Pgsql{}, want: `"users".*`},
		{name: "sqlite", build: &Sqlite{}, want: `"users".*`},
		{name: "sqlsrv", build: &Sqlsrv{}, want: `[users].*`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.build.QuoteFields("users.*"); got != testCase.want {
				t.Fatalf("限定通配符引用错误: got=%q want=%q", got, testCase.want)
			}
		})
	}
}

// TestOffsetOnlyPaginationIsValidForMysqlAndSQLite 验证 offset 不会生成缺少 LIMIT 的非法语法。
func TestOffsetOnlyPaginationIsValidForMysqlAndSQLite(t *testing.T) {
	_, mysqlLimit := (&Mysql{}).Pagination("", 0, 5)
	if mysqlLimit != " LIMIT 18446744073709551615 OFFSET 5" {
		t.Fatalf("MySQL offset-only 语法错误: %q", mysqlLimit)
	}
	_, sqliteLimit := (&Sqlite{}).Pagination("", 0, 5)
	if sqliteLimit != " LIMIT -1 OFFSET 5" {
		t.Fatalf("SQLite offset-only 语法错误: %q", sqliteLimit)
	}
}

// TestBuildersExposeBindParameterBudgets 验证批量写入不再依赖实现类型名猜测上限。
func TestBuildersExposeBindParameterBudgets(t *testing.T) {
	if (&Mysql{}).MaxBindParams() != 65535 || (&Pgsql{}).MaxBindParams() != 65535 ||
		(&Sqlite{}).MaxBindParams() != 999 || (&Sqlsrv{}).MaxBindParams() != 2100 {
		t.Fatal("方言绑定参数预算错误")
	}
}

// TestPgsqlRejectsUnknownLockMode 验证未知锁模式不会原样拼入 SQL。
func TestPgsqlRejectsUnknownLockMode(t *testing.T) {
	if got := (&Pgsql{}).LockClause("FOR UPDATE; DROP TABLE users"); got != "" {
		t.Fatalf("未知锁模式必须被忽略，实际为 %q", got)
	}
}
