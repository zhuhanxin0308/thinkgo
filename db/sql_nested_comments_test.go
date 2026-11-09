package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestPostgresNestedCommentBoundaries 同时验证脱敏与参数计数使用完整嵌套边界。
func TestPostgresNestedCommentBoundaries(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		query    string
		redacted string
		count    int
		invalid  bool
	}{
		{"双层", `SELECT /* outer /* inner */ marker-123 ? */ ?`, `SELECT /*[REDACTED]*/ ?`, 1, false},
		{"三层", `SELECT ? /* a /* b /* c ? */ hidden-b */ hidden-a ? */ , ?`, `SELECT ? /*[REDACTED]*/ , ?`, 2, false},
		{"注释内引号", `SELECT /* ' /* nested */ "secret" $tag$ ? */ ?`, `SELECT /*[REDACTED]*/ ?`, 1, false},
		{"相邻注释", `/* a /* b */ c */ ? /* d */ , ?`, `/*[REDACTED]*/ ? /*[REDACTED]*/ , ?`, 2, false},
		{"不重叠结束符", `SELECT /*/ ? */ ?`, `SELECT /*[REDACTED]*/ ?`, 1, false},
		{"字符串内注释", `SELECT '/* ? */', $q$/* ? */$q$, ?`, `SELECT '[REDACTED]', $q$[REDACTED]$q$, ?`, 1, false},
		{"未闭合外层", `SELECT /* a /* b */ hidden ?`, `SELECT /*[REDACTED]*/`, 0, true},
		{"未闭合内层", `SELECT /* a /* hidden ?`, `SELECT /*[REDACTED]*/`, 0, true},
		{"仅起始符", `SELECT /*`, `SELECT /*[REDACTED]*/`, 0, true},
	} {
		for _, dialect := range []string{"postgres", " POSTGRES "} {
			t.Run(testCase.name+"/"+dialect, func(t *testing.T) {
				count, err := countSQLPlaceholders(testCase.query, dialect)
				if (err != nil) != testCase.invalid || err == nil && count != testCase.count {
					t.Fatalf("参数计数不符: 数量=%d, 错误=%v", count, err)
				}
				if got := redactSQLLiteralsAndComments(testCase.query, dialect); got != testCase.redacted {
					t.Fatalf("脱敏边界不符: 实际=%q, 预期=%q", got, testCase.redacted)
				}
			})
		}
	}
	// 非 PostgreSQL 方言保留已有的单层块注释契约，不推断或扩大其他方言语法。
	for _, dialect := range []string{"mysql", "sqlite", "oracle", "sqlserver", ""} {
		query := `SELECT /* first /* inner */ tail ? */ ?`
		if count, err := countSQLPlaceholders(query, dialect); err != nil || count != 2 {
			t.Fatalf("%q 的既有单层计数被改变: %d, %v", dialect, count, err)
		}
		if got := redactSQLLiteralsAndComments(query, dialect); got != `SELECT /*[REDACTED]*/ tail ? */ ?` {
			t.Fatalf("%q 的既有边界被改变: %q", dialect, got)
		}
	}
}

// TestPostgresNestedCommentsReachExecutorAndStayRedacted 验证真实框架调用链中的校验、重绑定和 SQL 日志。
func TestPostgresNestedCommentsReachExecutorAndStayRedacted(t *testing.T) {
	database, state := newPostgresArrayValidationDatabase(t)
	logger := &queryDiagnosticLogger{}
	database.SetLogger(logger)
	database.queryLogging = true
	const comment = "/* outer /* inner ? */ synthetic-private ? */"
	if _, err := database.QueryContext(context.Background(), "SELECT "+comment+" ?", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecuteContext(context.Background(), "UPDATE records SET value = "+comment+" ?", 1); err != nil {
		t.Fatal(err)
	}
	_, _, operations := state.snapshot()
	if len(operations) != 2 || !strings.HasSuffix(operations[0].query, "$1") || !strings.HasSuffix(operations[1].query, "$1") {
		t.Fatalf("嵌套注释中的问号被错误重绑定: %#v", operations)
	}
	if calls := logger.calls(); len(calls) != 2 || strings.Contains(fmt.Sprint(calls), "synthetic-private") || !strings.Contains(fmt.Sprint(calls), sqlRedactionMarker) {
		t.Fatalf("SQL 跟踪未正确脱敏: %#v", calls)
	}
	errorLogger := &dbTestLogger{}
	database.SetLogger(errorLogger)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := database.QueryContext(canceled, "SELECT "+comment+" ?", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("应到达执行器并返回取消错误: %v", err)
	}
	if len(errorLogger.errorCalls) != 1 {
		t.Fatal("未记录真实执行器错误")
	}
	logged := fmt.Sprint(errorLogger.errorCalls[0].ctx["sql"])
	if strings.Contains(logged, "synthetic-private") || !strings.Contains(logged, sqlRedactionMarker) {
		t.Fatalf("原生 SQL 错误日志未脱敏完整注释: %s", logged)
	}
}
