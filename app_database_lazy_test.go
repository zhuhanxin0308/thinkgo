package framework

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type lazyAppDatabaseConnector struct {
	calls *atomic.Int32
}

func (connector *lazyAppDatabaseConnector) Connect(configuration db.Config) (db.Connection, error) {
	connector.calls.Add(1)
	return &appFakeManagerConnection{identity: db.NewConnectionID("lazy-" + configuration.Database)}, nil
}

// TestDefaultDatabasePolicyMatchesThinkPHPLazyConnection 验证默认项目只在启动时
// 校验数据库配置，并在业务首次解析默认连接时才真正调用连接器。
func TestDefaultDatabasePolicyMatchesThinkPHPLazyConnection(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	var calls atomic.Int32
	connectorName := fmt.Sprintf("lazy_app_%d", time.Now().UnixNano())
	if err := db.RegisterConnector(connectorName, &lazyAppDatabaseConnector{calls: &calls}); err != nil {
		t.Fatalf("注册惰性测试连接器失败: %v", err)
	}
	databaseConfig := fmt.Sprintf(`{
  "default": "mysql",
  "connections": {
    "mysql": {"type": %q, "database": "thinkphp"}
  }
}`, connectorName)
	if err := os.WriteFile(filepath.Join(basePath, "config", "database.json"), []byte(databaseConfig), 0o600); err != nil {
		t.Fatalf("写入数据库配置失败: %v", err)
	}

	application := NewApp(basePath)
	if err := application.Initialize(); err != nil {
		t.Fatalf("初始化应用失败: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	if application.databaseStartupPolicy != DatabaseStartupLazy {
		t.Fatalf("未显式配置时应使用 lazy 策略，实际为 %q", application.databaseStartupPolicy)
	}
	if calls.Load() != 0 || application.db != nil {
		t.Fatalf("应用启动不得连接数据库: calls=%d db=%#v", calls.Load(), application.db)
	}
	if report := application.health.Readiness(context.Background()); !report.Healthy() {
		t.Fatalf("尚未使用的惰性数据库不应让应用未就绪: %#v", report)
	}

	first, err := ResolveServiceAs[*db.DB](application, ServiceDB)
	if err != nil || first == nil {
		t.Fatalf("首次解析默认数据库失败: database=%#v err=%v", first, err)
	}
	second, err := ResolveServiceAs[*db.DB](application, ServiceDB)
	if err != nil || second != first {
		t.Fatalf("后续解析必须复用默认数据库: first=%#v second=%#v err=%v", first, second, err)
	}
	if application.DB() != first || calls.Load() != 1 {
		t.Fatalf("DB facade 应复用惰性连接: facade=%#v calls=%d", application.DB(), calls.Load())
	}
}
