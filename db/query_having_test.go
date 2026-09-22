package db

import "testing"

// TestHavingReplacementResetsArguments 验证替换 HAVING 条件时不会残留旧参数。
func TestHavingReplacementResetsArguments(t *testing.T) {
	query := NewDB(&mockConnection{}).Table("users").Having("total > ?", 1)
	query = query.Having("total < ?", 10)

	if query.err != nil {
		t.Fatalf("连续设置 Having 不应失败，实际错误: %v", query.err)
	}
	if query.having != "total < ?" {
		t.Fatalf("Having 应保留最后一次条件，实际为 %q", query.having)
	}
	if len(query.havingArgs) != 1 || query.havingArgs[0] != 10 {
		t.Fatalf("Having 应只保留最后一次参数，实际为 %#v", query.havingArgs)
	}
}

// TestHavingRawReplacementResetsArguments 验证替换原生 HAVING 条件时不会残留旧参数。
func TestHavingRawReplacementResetsArguments(t *testing.T) {
	query := NewDB(&mockConnection{}).Table("users").HavingRaw("SUM(total) > ?", 1)
	query = query.HavingRaw("SUM(total) < ?", 10)

	if query.err != nil {
		t.Fatalf("连续设置 HavingRaw 不应失败，实际错误: %v", query.err)
	}
	if query.having != "SUM(total) < ?" {
		t.Fatalf("HavingRaw 应保留最后一次条件，实际为 %q", query.having)
	}
	if len(query.havingArgs) != 1 || query.havingArgs[0] != 10 {
		t.Fatalf("HavingRaw 应只保留最后一次参数，实际为 %#v", query.havingArgs)
	}
}
