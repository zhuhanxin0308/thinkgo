package log

import (
	"testing"
	"time"
)

type timezoneCaptureDriver struct {
	entry *LogEntry
}

func (d *timezoneCaptureDriver) SaveEntries(entries []*LogEntry) error {
	if len(entries) > 0 {
		d.entry = entries[len(entries)-1]
	}
	return nil
}

func (d *timezoneCaptureDriver) WriteEntry(entry *LogEntry) error {
	d.entry = entry
	return nil
}

func (d *timezoneCaptureDriver) Close() error {
	return nil
}

// TestLogDefaultsToUTC 验证日志时间默认不读取部署机器的 Local。
func TestLogDefaultsToUTC(t *testing.T) {
	driver := &timezoneCaptureDriver{}
	logger := NewLog(driver)
	logger.SetLocation(nil)
	logger.Write("utc", LevelInfo)
	if driver.entry == nil || driver.entry.Time.Location() != time.UTC {
		t.Fatalf("日志条目默认时区必须为 UTC: entry=%#v", driver.entry)
	}
}

// TestLogUsesConfiguredLocation 验证日志条目的时间使用应用配置的时区。
func TestLogUsesConfiguredLocation(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}

	driver := &timezoneCaptureDriver{}
	logger := NewLog(driver)
	logger.SetLocation(location)
	logger.Write("timezone", LevelInfo)

	if driver.entry == nil {
		t.Fatal("日志驱动未收到条目")
	}
	if driver.entry.Time.Location().String() != "Asia/Shanghai" {
		t.Fatalf("日志条目时区错误: %q", driver.entry.Time.Location())
	}
}

// TestLogLocationPropagatesToDriversAddedLater 验证运行中新增的时区驱动也继承当前应用时区。
func TestLogLocationPropagatesToDriversAddedLater(t *testing.T) {
	location, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}

	driver := &timezoneCaptureDriver{}
	logger := NewLog()
	logger.SetLocation(location)
	if err := logger.AddDriver(driver); err != nil {
		t.Fatalf("新增日志驱动失败: %v", err)
	}
	logger.Write("timezone", LevelInfo)
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭日志器失败: %v", err)
	}

	if driver.entry == nil || driver.entry.Time.Location().String() != "Asia/Tokyo" {
		t.Fatalf("新增日志驱动未继承应用时区: entry=%#v", driver.entry)
	}
}

// TestLogLocationPropagatesToChannels 验证日志通道不会回退到主机时区。
func TestLogLocationPropagatesToChannels(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}

	parent := NewLog()
	driver := &timezoneCaptureDriver{}
	child := NewLog(driver)
	if err := parent.RegisterChannel("sql", child); err != nil {
		t.Fatalf("注册日志通道失败: %v", err)
	}
	parent.SetLocation(location)
	child.Write("timezone", LevelInfo)

	if driver.entry == nil || driver.entry.Time.Location().String() != "America/New_York" {
		t.Fatalf("日志通道未继承应用时区: entry=%#v", driver.entry)
	}
}
