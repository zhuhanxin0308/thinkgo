package driver

import (
	"reflect"
	"testing"
	"time"

	"thinkgo/framework/log"
)

// currentHandle 通过反射读取 File 驱动当前缓存的文件句柄指针，便于测试句柄复用与释放。
func currentHandle(driver *File) (uintptr, bool) {
	field := reflect.ValueOf(driver).Elem().FieldByName("currentFile")
	if !field.IsValid() {
		return 0, false
	}
	if field.IsNil() {
		return 0, true
	}
	return field.Pointer(), true
}

// TestFileDriverCachesOpenFileHandle 验证同一日志文件会复用已打开的句柄，避免每条日志重复 open/close。
func TestFileDriverCachesOpenFileHandle(t *testing.T) {
	dir := t.TempDir()
	driver := NewFile(dir)
	t.Cleanup(func() {
		_ = driver.Close()
	})

	entryTime := time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local)
	entry := &log.LogEntry{Time: entryTime, Level: "info", Message: "first"}
	if err := driver.WriteEntry(entry); err != nil {
		t.Fatalf("第一次写日志失败: %v", err)
	}

	before, ok := currentHandle(driver)
	if !ok {
		t.Fatal("File 驱动应维护当前文件句柄")
	}
	if before == 0 {
		t.Fatal("第一次写入后应缓存当前日志文件句柄")
	}

	secondEntry := &log.LogEntry{Time: entryTime, Level: "info", Message: "second"}
	if err := driver.WriteEntry(secondEntry); err != nil {
		t.Fatalf("第二次写日志失败: %v", err)
	}

	after, _ := currentHandle(driver)
	if after == 0 {
		t.Fatal("第二次写入后缓存句柄不应丢失")
	}
	if before != after {
		t.Fatal("同一日志文件应复用同一个已打开句柄")
	}
}

// TestFileDriverCloseReleasesCachedHandles 验证 Close 会释放缓存的文件句柄。
func TestFileDriverCloseReleasesCachedHandles(t *testing.T) {
	dir := t.TempDir()
	driver := NewFile(dir)

	entry := &log.LogEntry{
		Time:    time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local),
		Level:   "info",
		Message: "close",
	}
	if err := driver.WriteEntry(entry); err != nil {
		t.Fatalf("写日志失败: %v", err)
	}

	if err := driver.Close(); err != nil {
		t.Fatalf("关闭日志驱动失败: %v", err)
	}

	handle, ok := currentHandle(driver)
	if !ok {
		t.Fatal("File 驱动应维护当前文件句柄字段")
	}
	if handle != 0 {
		t.Fatal("Close 后应释放并清空当前文件句柄")
	}
}

// TestFileDriverRotatesHandleAcrossDays 验证跨天写入会关闭旧句柄、切换到新文件，避免句柄随天数泄漏。
func TestFileDriverRotatesHandleAcrossDays(t *testing.T) {
	dir := t.TempDir()
	driver := NewFile(dir)
	t.Cleanup(func() {
		_ = driver.Close()
	})

	day1 := time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local)
	if err := driver.WriteEntry(&log.LogEntry{Time: day1, Level: "info", Message: "day1"}); err != nil {
		t.Fatalf("写第一天日志失败: %v", err)
	}
	first, _ := currentHandle(driver)

	day2 := time.Date(2026, 4, 16, 12, 0, 0, 0, time.Local)
	if err := driver.WriteEntry(&log.LogEntry{Time: day2, Level: "info", Message: "day2"}); err != nil {
		t.Fatalf("写第二天日志失败: %v", err)
	}
	second, _ := currentHandle(driver)

	if second == 0 {
		t.Fatal("跨天写入后应持有新文件句柄")
	}
	if first == second {
		t.Fatal("跨天写入应切换到新文件句柄，而非复用旧句柄")
	}

	nameField := reflect.ValueOf(driver).Elem().FieldByName("currentName")
	if got := nameField.String(); got != driver.getLogFile(day2) {
		t.Fatalf("当前句柄应指向第二天日志文件，实际 %s", got)
	}
}
