package log

import "testing"

// TestSQLContextUsesSQLLevel 验证结构化 SQL 日志保留 SQL 级别和上下文。
func TestSQLContextUsesSQLLevel(t *testing.T) {
	driver := &mockDriver{}
	logger := NewLog()
	if err := logger.AddDriver(driver); err != nil {
		t.Fatalf("添加日志驱动失败: %v", err)
	}
	logger.SqlCtx("database operation", map[string]interface{}{"operation": "select"})
	closeTestLogger(t, logger)

	entries := driver.allEntries()
	if len(entries) != 1 {
		t.Fatalf("SQL 上下文日志数量错误: %d", len(entries))
	}
	if entries[0].Level != LevelSQL || entries[0].Context["operation"] != "select" {
		t.Fatalf("SQL 上下文日志错误: %#v", entries[0])
	}
}
