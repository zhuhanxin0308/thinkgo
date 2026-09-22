package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type appFakeManagerConnector struct{}

type appFakeManagerConnection struct {
	identity db.ConnectionID
}

func (c *appFakeManagerConnector) Connect(config db.Config) (db.Connection, error) {
	return &appFakeManagerConnection{identity: db.NewConnectionID("app-manager-test")}, nil
}

func (c *appFakeManagerConnection) ConnectionID() db.ConnectionID {
	return c.identity
}

func (c *appFakeManagerConnection) Select(context.Context, db.SelectRequest) ([]map[string]interface{}, error) {
	return nil, nil
}

func (c *appFakeManagerConnection) Insert(context.Context, db.InsertRequest) (db.InsertResult, error) {
	return db.InsertResult{}, nil
}

func (c *appFakeManagerConnection) Update(context.Context, db.UpdateRequest) (db.UpdateResult, error) {
	return db.UpdateResult{}, nil
}

func (c *appFakeManagerConnection) Delete(context.Context, db.DeleteRequest) (db.DeleteResult, error) {
	return db.DeleteResult{}, nil
}

func (c *appFakeManagerConnection) Count(context.Context, db.CountRequest) (int64, error) {
	return 0, nil
}

func (c *appFakeManagerConnection) Close() error {
	return nil
}

func TestAppInitializesMultiDatabaseManager(t *testing.T) {
	connectorName := fmt.Sprintf("test-manager-%d", time.Now().UnixNano())
	if err := db.RegisterConnector(connectorName, &appFakeManagerConnector{}); err != nil {
		t.Fatalf("注册多数据库测试连接器失败: %v", err)
	}

	baseDir := t.TempDir()
	writeTestAppConfigFiles(t, baseDir)
	setDatabaseStartupPolicyForTest(t, baseDir, DatabaseStartupRequired)
	configDir := filepath.Join(baseDir, "config")

	databaseConfig := map[string]interface{}{
		"default": "primary",
		"connections": map[string]interface{}{
			"primary": map[string]interface{}{
				"type":     connectorName,
				"database": "primary-db",
			},
			"analytics": map[string]interface{}{
				"type":     connectorName,
				"database": "analytics-db",
			},
		},
	}
	content, err := json.Marshal(databaseConfig)
	if err != nil {
		t.Fatalf("序列化数据库配置失败，错误为 %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "database.json"), content, 0o644); err != nil {
		t.Fatalf("写入数据库配置失败，错误为 %v", err)
	}

	app := mustBuildTestApp(t, baseDir)
	t.Cleanup(func() { _ = app.Close() })
	if app.db == nil {
		t.Fatal("默认数据库连接应初始化成功")
	}
	if app.dbManager == nil {
		t.Fatal("多数据库管理器应初始化成功")
	}
	connection, err := app.dbManager.Connection("analytics")
	if err != nil {
		t.Fatalf("analytics 连接应可读取，错误为 %v", err)
	}
	if connection == nil {
		t.Fatal("analytics 连接实例不应为空")
	}
}
