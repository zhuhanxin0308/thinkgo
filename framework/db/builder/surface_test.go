package builder

import (
	"strings"
	"testing"
)

type dialectSurface interface {
	Select(string, string, []string, string, int, int) string
	Delete(string, []string) string
	Count(string, []string) string
	LockClause(string) string
	InsertReturning(string, map[string]interface{}, string) (string, []interface{}, bool)
	SupportsLastInsertId() bool
}

func TestDialectQuerySurfaceCoversEverySupportedBackend(t *testing.T) {
	cases := []struct {
		name              string
		builder           dialectSurface
		expectsReturning  bool
		expectsLastInsert bool
		expectsLock       string
	}{
		{name: "mysql", builder: &Mysql{}, expectsReturning: false, expectsLastInsert: true, expectsLock: " FOR UPDATE"},
		{name: "pgsql", builder: &Pgsql{}, expectsReturning: true, expectsLastInsert: false, expectsLock: " FOR UPDATE"},
		{name: "sqlite", builder: &Sqlite{}, expectsReturning: false, expectsLastInsert: true, expectsLock: ""},
		{name: "sqlsrv", builder: &Sqlsrv{}, expectsReturning: true, expectsLastInsert: false, expectsLock: ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			selectSQL := test.builder.Select("users", "id, name", []string{"active = ?"}, "name DESC", 10, 2)
			if !strings.HasPrefix(selectSQL, "SELECT ") || !strings.Contains(selectSQL, "WHERE active = ?") ||
				(!strings.Contains(selectSQL, "LIMIT") && !strings.Contains(selectSQL, "OFFSET")) {
				t.Fatalf("SELECT SQL 不完整: %q", selectSQL)
			}
			deleteSQL := test.builder.Delete("users", []string{"id = ?"})
			if !strings.HasPrefix(deleteSQL, "DELETE FROM ") || !strings.Contains(deleteSQL, "WHERE id = ?") {
				t.Fatalf("DELETE SQL 不完整: %q", deleteSQL)
			}
			countSQL := test.builder.Count("users", []string{"active = ?"})
			if !strings.HasPrefix(countSQL, "SELECT COUNT(*) FROM ") || !strings.Contains(countSQL, "WHERE active = ?") {
				t.Fatalf("COUNT SQL 不完整: %q", countSQL)
			}
			if got := test.builder.LockClause("FOR UPDATE"); got != test.expectsLock {
				t.Fatalf("锁子句错误: got=%q want=%q", got, test.expectsLock)
			}
			query, values, returning := test.builder.InsertReturning("users", map[string]interface{}{"name": "alice"}, "id")
			if returning != test.expectsReturning || (returning && (query == "" || len(values) != 1)) || (!returning && (query != "" || values != nil)) {
				t.Fatalf("InsertReturning 结果错误: query=%q values=%#v returning=%t", query, values, returning)
			}
			if test.builder.SupportsLastInsertId() != test.expectsLastInsert {
				t.Fatal("LastInsertId 能力声明错误")
			}
		})
	}
}

func TestBuilderQuotingAndRebindEdgeCases(t *testing.T) {
	if got := quoteOrderWith("name SIDEWAYS, ,id ASC extra", "`", "`"); got != "`name SIDEWAYS`, `id ASC extra`" {
		t.Fatalf("非法排序表达式应整体引用: %q", got)
	}
	if got := quoteOrderWith("name, id DESC", "`", "`"); got != "`name`, `id` DESC" {
		t.Fatalf("合法排序表达式引用错误: %q", got)
	}
	if got := quoteOrderWith("   ", "`", "`"); got != "" {
		t.Fatalf("空排序表达式应为空: %q", got)
	}

	for _, test := range []struct {
		text string
		want string
	}{
		{text: "$tag$?$tag$ ?", want: "$tag$?$tag$ @p1"},
		{text: "$bad-tag$ ?", want: "$bad-tag$ @p1"},
		{text: "SELECT [a?] AS `b?`, ?", want: "SELECT [a?] AS `b?`, @p1"},
	} {
		if got := (&Sqlsrv{}).Rebind(test.text); got != test.want {
			t.Fatalf("SQL Server Rebind 边界错误: got=%q want=%q", got, test.want)
		}
	}
	if got := builderDollarDelimiter("$tag$"); got != "$tag$" {
		t.Fatalf("合法 dollar quote 分隔符错误: %q", got)
	}
	for _, invalid := range []string{"$", "$bad-tag$", "$bad"} {
		if got := builderDollarDelimiter(invalid); got != "" {
			t.Fatalf("非法 dollar quote 不应被识别: input=%q got=%q", invalid, got)
		}
	}
}
