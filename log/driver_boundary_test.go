package log

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

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
