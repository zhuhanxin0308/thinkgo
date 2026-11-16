package db

import (
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

func TestSQLServerNestedBlockCommentsSharePlaceholderAndRedactionBoundaries(t *testing.T) {
	const query = `SELECT ? /* outer /* inner ? */ private-comment ? */, ? AS [n?]`
	const want = `SELECT @p1 /* outer /* inner ? */ private-comment ? */, @p2 AS [n?]`
	if err := validateRawStatement(query, 2, "sqlserver"); err != nil {
		t.Errorf("SQL Server 应忽略整个嵌套注释中的占位符: %v", err)
	}
	if got := (&builder.Sqlsrv{}).Rebind(query); got != want {
		t.Errorf("重绑定使用了不同的注释边界: %q，期望 %q", got, want)
	}
	if got := redactSQLLiteralsAndComments(query, "sqlserver"); strings.Contains(got, "private-comment") {
		t.Errorf("脱敏暴露了外层注释的后半部分: %q", got)
	}
	const incomplete = `SELECT ? /* outer /* inner ? */ remaining`
	if err := validateRawStatement(incomplete, 1, "sqlserver"); err == nil {
		t.Error("缺少最外层结束符的 T-SQL 必须拒绝")
	}
}
