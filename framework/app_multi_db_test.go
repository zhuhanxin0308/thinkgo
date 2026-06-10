package framework

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"thinkgo/framework/db"
)

type appFakeManagerConnector struct{}

type appFakeManagerConnection struct{}

func (c *appFakeManagerConnector) Connect(config db.Config) (db.Connection, error) {
	return &appFakeManagerConnection{}, nil
}

func (c *appFakeManagerConnection) Select(table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	return nil, nil
}

func (c *appFakeManagerConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	return 0, nil
}

func (c *appFakeManagerConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *appFakeManagerConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *appFakeManagerConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *appFakeManagerConnection) Close() error {
	return nil
}

func TestAppInitializesMultiDatabaseManager(t *testing.T) {
	db.RegisterConnector("test-manager", &appFakeManagerConnector{})

	baseDir := t.TempDir()
	configDir := filepath.Join(baseDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("创建配置目录失败，错误为 %v", err)
	}

	databaseConfig := map[string]interface{}{
		"default": "primary",
		"connections": map[string]interface{}{
			"primary": map[string]interface{}{
				"type":     "test-manager",
				"database": "primary-db",
			},
			"analytics": map[string]interface{}{
				"type":     "test-manager",
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

	app := NewApp(baseDir)
	if app.DB == nil {
		t.Fatal("默认数据库连接应初始化成功")
	}
	if app.DBManager == nil {
		t.Fatal("多数据库管理器应初始化成功")
	}
	connection, err := app.DBManager.Connection("analytics")
	if err != nil {
		t.Fatalf("analytics 连接应可读取，错误为 %v", err)
	}
	if connection == nil {
		t.Fatal("analytics 连接实例不应为空")
	}
}
