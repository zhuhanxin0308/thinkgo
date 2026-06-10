package log

import (
	"testing"
	"time"
)

func TestLogChannelsAreIsolated(t *testing.T) {
	defaultDriver := newMockDriver()
	sqlDriver := newMockDriver()

	logger := NewLog(defaultDriver)
	sqlChannel := NewLog(sqlDriver)
	sqlChannel.SetFlushInterval(50 * time.Millisecond)
	logger.RegisterChannel("sql", sqlChannel)
	logger.SetFlushInterval(50 * time.Millisecond)

	logger.Info("default")
	logger.Channel("sql").Info("sql")

	time.Sleep(100 * time.Millisecond)
	logger.Shutdown()

	if defaultDriver.totalSavedCount() != 1 {
		t.Fatalf("默认通道应仅收到 1 条日志，实际为 %d", defaultDriver.totalSavedCount())
	}
	if sqlDriver.totalSavedCount() != 1 {
		t.Fatalf("sql 通道应仅收到 1 条日志，实际为 %d", sqlDriver.totalSavedCount())
	}

	defaultEntries := defaultDriver.allEntries()
	if len(defaultEntries) != 1 || defaultEntries[0].Message != "default" {
		t.Fatalf("默认通道日志内容不正确，实际为 %#v", defaultEntries)
	}
	sqlEntries := sqlDriver.allEntries()
	if len(sqlEntries) != 1 || sqlEntries[0].Message != "sql" {
		t.Fatalf("sql 通道日志内容不正确，实际为 %#v", sqlEntries)
	}
}
