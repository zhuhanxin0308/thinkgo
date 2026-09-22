package framework

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/env"
	frameworkLog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

type secretFailureDatabaseConnector struct{}

func (*secretFailureDatabaseConnector) Connect(db.Config) (db.Connection, error) {
	return nil, errors.New("dial postgres://user:uri-secret@db.internal/app password=plain-secret token=token-secret")
}

type databaseWarningCaptureDriver struct {
	mu      sync.Mutex
	entries []*frameworkLog.LogEntry
}

func (d *databaseWarningCaptureDriver) SaveEntries(entries []*frameworkLog.LogEntry) error {
	d.mu.Lock()
	d.entries = append(d.entries, entries...)
	d.mu.Unlock()
	return nil
}

func (d *databaseWarningCaptureDriver) WriteEntry(entry *frameworkLog.LogEntry) error {
	return d.SaveEntries([]*frameworkLog.LogEntry{entry})
}

func (*databaseWarningCaptureDriver) Close() error { return nil }

func (d *databaseWarningCaptureDriver) messages() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var builder strings.Builder
	for _, entry := range d.entries {
		if entry != nil {
			builder.WriteString(entry.Message)
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

// TestDatabaseConnectionWarningsRedactCredentials 验证连接器错误日志保留连接名但不泄露 DSN 凭据。
func TestDatabaseConnectionWarningsRedactCredentials(t *testing.T) {
	connectorName := fmt.Sprintf("secret_failure_%d", time.Now().UnixNano())
	if err := db.RegisterConnector(connectorName, &secretFailureDatabaseConnector{}); err != nil {
		t.Fatalf("注册敏感错误测试连接器失败: %v", err)
	}
	driver := &databaseWarningCaptureDriver{}
	logger := frameworkLog.NewLog(driver)
	t.Cleanup(func() { _ = logger.Close() })
	app := &App{dbManager: db.NewManager("primary"), env: env.NewEnv(), log: logger}
	app.initDatabaseConnections(map[string]interface{}{
		"connections": map[string]interface{}{
			"primary": map[string]interface{}{"type": connectorName, "database": "app"},
		},
	}, "primary")
	if err := logger.Flush(context.Background()); err != nil {
		t.Fatalf("刷新数据库连接警告失败: %v", err)
	}
	message := driver.messages()
	if !strings.Contains(message, `数据库连接 "primary" 失败`) {
		t.Fatalf("连接警告应保留连接名，实际为 %q", message)
	}
	for _, secret := range []string{"uri-secret", "plain-secret", "token-secret"} {
		if strings.Contains(message, secret) {
			t.Fatalf("连接警告泄露敏感值 %q，实际为 %q", secret, message)
		}
	}
	if !strings.Contains(message, "[REDACTED]") {
		t.Fatalf("连接警告应显示脱敏占位符，实际为 %q", message)
	}
}
