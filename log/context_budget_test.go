package log

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestLogContextBoundsSharedGraphExpansion 验证少量共享对象不能展开为不受控的日志正文。
func TestLogContextBoundsSharedGraphExpansion(t *testing.T) {
	shared := map[string]interface{}{"value": "visible", "password": "test-only-secret"}
	for depth := 0; depth < 16; depth++ {
		shared = map[string]interface{}{"left": shared, "right": shared}
	}
	entry := newLogEntry(time.Time{}, LevelInfo, "bounded event", shared)
	formatted := entry.FormatEntry()
	if !strings.Contains(formatted, "[TRUNCATED]") || !strings.Contains(formatted, "bounded event") {
		t.Error("超预算上下文必须显式截断并保留事件消息")
	}
	if len(formatted) > 1<<20 || strings.Contains(formatted, "test-only-secret") {
		t.Errorf("日志仍存在无限展开或凭据泄漏: bytes=%d", len(formatted))
	}
	first, second := entry.forDriver(), entry.forDriver()
	first.Context["log_context"] = "changed by driver"
	if second.Context["log_context"] != "[TRUNCATED]" || strings.Contains(entry.FormatEntry(), "changed by driver") {
		t.Fatal("截断后的驱动快照失去隔离")
	}
}

// TestLogContextBoundsDeepAndWideInputs 验证预算覆盖深度、大集合、长字段名及长字符串。
func TestLogContextBoundsDeepAndWideInputs(t *testing.T) {
	deep := map[string]interface{}{"value": "leaf"}
	for depth := 0; depth < 128; depth++ {
		deep = map[string]interface{}{"next": deep}
	}
	for name, fields := range map[string]map[string]interface{}{
		"深层链": deep,
		"长数组": {"values": make([]int, 1<<16)},
		"长值":  {"value": strings.Repeat("x", 1<<20)},
		"长键":  {strings.Repeat("x", 1<<20): "value"},
	} {
		t.Run(name, func(t *testing.T) {
			encoded := marshalSanitizedContext(fields)
			if !json.Valid([]byte(encoded)) || !strings.Contains(encoded, "[TRUNCATED]") || len(encoded) > 1<<20 {
				t.Errorf("异常形状未生成有界的合法 JSON: bytes=%d", len(encoded))
			}
		})
	}
}

// TestLogContextBudgetPreservesSmallValues 验证正常共享结构仍按原字段完整输出并保持深度脱敏。
func TestLogContextBudgetPreservesSmallValues(t *testing.T) {
	shared := map[string]interface{}{"name": "before", "token": "test-secret"}
	fields := map[string]interface{}{"left": shared, "right": shared}
	entry := newLogEntry(time.Time{}, LevelInfo, "normal event", fields)
	shared["name"] = "after"
	formatted := entry.FormatEntry()
	if strings.Contains(formatted, "[TRUNCATED]") || strings.Contains(formatted, "test-secret") || strings.Contains(formatted, "after") {
		t.Fatal("正常快照的完整性或隔离契约被破坏")
	}
	if strings.Count(formatted, "before") != 2 || strings.Count(formatted, logRedactedPlaceholder) != 2 {
		t.Fatalf("正常共享引用应独立展开并脱敏: %s", formatted)
	}
}

// TestRecordContextKeepsBoundedSnapshot 验证公开同步准备、异步入队及驱动输出都遵循同一预算。
func TestRecordContextKeepsBoundedSnapshot(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	t.Cleanup(func() {
		if err := logger.Close(); err != nil {
			t.Error(err)
		}
	})
	fields := map[string]interface{}{"values": make([]int, 1<<16)}
	if err := logger.RecordContext(context.Background(), "queued event", LevelInfo, fields); err != nil {
		t.Fatal(err)
	}
	if err := logger.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := driver.allEntries()
	if len(entries) != 1 || !strings.Contains(entries[0].FormatEntry(), "[TRUNCATED]") {
		t.Fatal("异步驱动未收到受控截断标记")
	}
}
