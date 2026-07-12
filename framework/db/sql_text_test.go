package db

import "testing"

// TestCountSQLPlaceholdersIgnoresQuotedAndCommentedQuestionMarks 验证原生 SQL 参数计数不误判字面量。
func TestCountSQLPlaceholdersIgnoresQuotedAndCommentedQuestionMarks(t *testing.T) {
	tests := []struct {
		sqlText string
		want    int
	}{
		{sqlText: `name = '?' AND id = ?`, want: 1},
		{sqlText: `"question?" = "question?" AND id = ?`, want: 1},
		{sqlText: "id = ? -- ignored ?\nAND state = ?", want: 2},
		{sqlText: `id = ? /* ignored ? */ AND state = ?`, want: 2},
		{sqlText: `body = $tag$?$tag$ AND id = ?`, want: 1},
		{sqlText: `[question?] = [question?] AND id = ?`, want: 1},
		{sqlText: `payload ?? 'role'`, want: 0},
		{sqlText: `payload ?? ?`, want: 1},
		{sqlText: `tags ?| ? AND required ?& ?`, want: 2},
		{sqlText: `payload @? ? AND tenant_id = ?`, want: 2},
	}
	for _, testCase := range tests {
		got, err := countSQLPlaceholders(testCase.sqlText)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", testCase.sqlText, err)
		}
		if got != testCase.want {
			t.Fatalf("SQL %q 占位符数量错误，期望 %d，实际 %d", testCase.sqlText, testCase.want, got)
		}
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
