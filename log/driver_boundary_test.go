package log

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type nilPanicLogDriver struct{}

func (nilPanicLogDriver) WriteEntry(*LogEntry) error    { panic(nil) }
func (nilPanicLogDriver) SaveEntries([]*LogEntry) error { panic(nil) }
func (nilPanicLogDriver) Close() error                  { panic(nil) }
func (nilPanicLogDriver) Write([]byte) (int, error)     { panic(nil) }

func TestLogReportsLegacyNilPanic(t *testing.T) {
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",panicnil=1")
	driver := nilPanicLogDriver{}
	logger := NewLog(driver)
	var fallback bytes.Buffer
	logger.SetFallbackWriter(&fallback)
	logger.Write("同步消息", "error")
	logger.Info("异步消息")
	if err := logger.Flush(t.Context()); err == nil || !strings.Contains(err.Error(), "SaveEntries") {
		t.Errorf("刷新未报告批次持久化失败：%v", err)
	}
	if err := logger.Close(); err == nil || !strings.Contains(err.Error(), "Close") {
		t.Errorf("关闭未报告资源释放失败：%v", err)
	}
	if logger.DriverErrorCount() != 3 || !strings.Contains(fallback.String(), "WriteEntry") {
		t.Errorf("写入失败诊断不完整：次数=%d，兜底输出=%q", logger.DriverErrorCount(), fallback.String())
	}
	if err := safeFallbackWrite(driver, "诊断"); err == nil {
		t.Fatal("兜底输出异常被当作成功")
	}
}

type mutatingContextDriver struct{ captured []byte }

func (driver *mutatingContextDriver) WriteEntry(entry *LogEntry) error {
	driver.captured, _ = json.Marshal(entry.Context)
	entry.Context["safe"].(map[string]any)["name"] = "changed"
	entry.Message = "changed"
	return nil
}
func (driver *mutatingContextDriver) SaveEntries(entries []*LogEntry) error {
	for _, entry := range entries {
		_ = driver.WriteEntry(entry)
	}
	return nil
}
func (*mutatingContextDriver) Close() error { return nil }

// TestDriverBoundaryRedactsAndIsolates 验证自定义驱动拿不到原始凭据，也不能污染后续驱动的数据。
func TestDriverBoundaryRedactsAndIsolates(t *testing.T) {
	for _, mode := range []string{"record", "context", "nonblocking"} {
		t.Run(mode, func(t *testing.T) {
			first, second := &mutatingContextDriver{}, newMockDriver()
			logger := NewLog(first, second)
			fields := map[string]any{"password": "private-secret", "safe": map[string]any{"name": "original", "api_token": "nested-secret"}}
			switch mode {
			case "record":
				logger.InfoCtx("original", fields)
			case "context":
				if err := logger.RecordContext(context.Background(), "original", LevelInfo, fields); err != nil {
					t.Fatal(err)
				}
			case "nonblocking":
				if err := logger.RecordContextNonBlocking(context.Background(), "original", LevelInfo, fields); err != nil {
					t.Fatal(err)
				}
			}
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"private-secret", "nested-secret"} {
				if strings.Contains(string(first.captured), secret) {
					t.Fatalf("驱动收到原始凭据: %s", first.captured)
				}
			}
			entries := second.allEntries()
			if len(entries) != 1 || entries[0].Message != "original" || entries[0].Context["safe"].(map[string]any)["name"] != "original" {
				t.Fatalf("驱动之间共享可变条目: %#v", entries)
			}
			if fields["password"] != "private-secret" || fields["safe"].(map[string]any)["name"] != "original" {
				t.Fatal("修改了调用方上下文")
			}
		})
	}
}
