package db

import (
	"strings"
	"testing"
)

// TestCountSQLPlaceholdersIgnoresQuotedAndCommentedQuestionMarks 验证原生 SQL 参数计数不误判字面量。
func TestCountSQLPlaceholdersIgnoresQuotedAndCommentedQuestionMarks(t *testing.T) {
	tests := []struct {
		sqlText string
		want    int
		dialect string
	}{
		{sqlText: `name = '?' AND id = ?`, want: 1},
		{sqlText: `"question?" = "question?" AND id = ?`, want: 1},
		{sqlText: "id = ? -- ignored ?\nAND state = ?", want: 2},
		{sqlText: `id = ? /* ignored ? */ AND state = ?`, want: 2},
		{sqlText: `body = $tag$?$tag$ AND id = ?`, want: 1},
		{sqlText: `[question?] = [question?] AND id = ?`, want: 1},
		{sqlText: `payload ?? 'role'`, want: 0, dialect: "postgres"},
		{sqlText: `payload ?? ?`, want: 1, dialect: "postgres"},
		{sqlText: `tags ?| ? AND required ?& ?`, want: 2, dialect: "postgres"},
		{sqlText: `payload @? ? AND tenant_id = ?`, want: 2, dialect: "postgres"},
	}
	for _, testCase := range tests {
		got, err := countSQLPlaceholders(testCase.sqlText, testCase.dialect)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", testCase.sqlText, err)
		}
		if got != testCase.want {
			t.Fatalf("SQL %q 占位符数量错误，期望 %d，实际 %d", testCase.sqlText, testCase.want, got)
		}
	}
}

// TestNonPostgresQuestionOperatorsAreBindings 验证其他方言应保留按位置绑定的问号。
func TestNonPostgresQuestionOperatorsAreBindings(t *testing.T) {
	const expression = `SELECT ?|? AS bit_or, ?&? AS bit_and`
	for _, dialect := range []string{"sqlite", "mysql", "sqlserver", "oracle"} {
		if err := validateRawStatement(expression, 4, dialect); err != nil {
			t.Errorf("%s 未保留 SQL 正文中的四个占位符: %v", dialect, err)
		}
	}
	const pgExpression = `SELECT payload ?? ?, payload ?| ?, payload ?& ?, payload @? ?`
	if err := validateRawStatement(pgExpression, 4, "postgres"); err != nil {
		t.Fatalf("PostgreSQL 问号操作符应保持原有语义: %v", err)
	}
}

// TestStandardSQLBackslashKeepsLiteralBoundary 验证路径末尾的反斜杠不吞掉引号或后续参数。
func TestStandardSQLBackslashKeepsLiteralBoundary(t *testing.T) {
	const query = `SELECT 'C:\' AS dir, 'AUDIT_CANARY' AS marker, ? AS bound`
	const redacted = `SELECT '[REDACTED]' AS dir, '[REDACTED]' AS marker, ? AS bound`
	for _, dialect := range []string{"oracle", "sqlserver"} {
		t.Run(dialect, func(t *testing.T) {
			sql, want := query, redacted
			if dialect == "oracle" {
				sql, want = sql+" FROM dual", want+" FROM dual"
			}
			if err := validateRawStatement(sql, 1, dialect); err != nil {
				t.Errorf("合法方言查询未通过参数校验: %v", err)
			}
			if got := redactSQLText(sql, dialect); got != want || strings.Contains(got, "AUDIT_CANARY") {
				t.Errorf("日志应隔离两个字面量: %q", got)
			}
		})
	}
}

// TestOracleAlternativeQuotedLiteralsKeepTheirBoundaries 验证 q 和 nq 字面量中的引号与问号保持为数据。
func TestOracleAlternativeQuotedLiteralsKeepTheirBoundaries(t *testing.T) {
	for _, literal := range []string{
		`q'[prefix\'AUDIT_CANARY\'? suffix]'`,
		`NQ'!first' AUDIT_CANARY ? second!' `,
		`Q'界first' AUDIT_CANARY ? second界'`,
	} {
		query := "SELECT " + literal + ", ? FROM dual"
		if err := validateRawStatement(query, 1, "oracle"); err != nil {
			t.Errorf("Oracle 替代引号中的问号不应计为参数: %v", err)
		}
		got := redactSQLText(query, "oracle")
		if strings.Contains(got, "AUDIT_CANARY") || !strings.Contains(got, sqlRedactionMarker) {
			t.Errorf("Oracle 替代引号内容应全部脱敏: %q", got)
		}
	}
	if err := validateRawStatement(`SELECT q'[unclosed' from dual`, 0, "oracle"); err == nil {
		t.Error("没有结束标记的 Oracle 替代引号应被拒绝")
	}
}

// TestCountSQLPlaceholdersRejectsUnclosedLexicalStates 验证截断引用和注释会被拒绝。
func TestCountSQLPlaceholdersRejectsUnclosedLexicalStates(t *testing.T) {
	for _, sqlText := range []string{`name = 'broken`, `id = ? /* broken`, `body = $tag$broken`} {
		if _, err := countSQLPlaceholders(sqlText); err == nil {
			t.Fatalf("未闭合 SQL 应返回错误: %q", sqlText)
		}
	}
}
