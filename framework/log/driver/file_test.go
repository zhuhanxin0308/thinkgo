package driver

import (
	"reflect"
	"testing"
	"time"

	"thinkgo/framework/log"
)

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

	openFilesField := reflect.ValueOf(driver).Elem().FieldByName("openFiles")
	if !openFilesField.IsValid() {
		t.Fatal("File 驱动应维护已打开文件句柄缓存")
	}
	if openFilesField.Len() != 1 {
		t.Fatalf("第一次写入后应缓存 1 个文件句柄，实际为 %d", openFilesField.Len())
	}

	cachedBefore := openFilesField.MapIndex(reflect.ValueOf(driver.getLogFile(entryTime)))
	if !cachedBefore.IsValid() || cachedBefore.IsNil() {
		t.Fatal("第一次写入后应缓存当前日志文件句柄")
	}

	secondEntry := &log.LogEntry{Time: entryTime, Level: "info", Message: "second"}
	if err := driver.WriteEntry(secondEntry); err != nil {
		t.Fatalf("第二次写日志失败: %v", err)
	}

	openFilesField = reflect.ValueOf(driver).Elem().FieldByName("openFiles")
	cachedAfter := openFilesField.MapIndex(reflect.ValueOf(driver.getLogFile(entryTime)))
	if !cachedAfter.IsValid() || cachedAfter.IsNil() {
		t.Fatal("第二次写入后缓存句柄不应丢失")
	}
	if cachedBefore.Pointer() != cachedAfter.Pointer() {
		t.Fatal("同一日志文件应复用同一个已打开句柄")
	}
}

// TestFileDriverCloseReleasesCachedHandles 验证 Close 会释放所有缓存文件句柄。
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

	openFilesField := reflect.ValueOf(driver).Elem().FieldByName("openFiles")
	if !openFilesField.IsValid() {
		t.Fatal("File 驱动应维护已打开文件句柄缓存")
	}
	if openFilesField.Len() != 0 {
		t.Fatalf("Close 后应清空句柄缓存，实际剩余 %d 个", openFilesField.Len())
	}
}
