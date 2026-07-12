package framework

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thinkgo/framework/db"
	"thinkgo/framework/env"
)

// TestReadDatabaseConfigRejectsUnknownAndWrongTypedFields 验证配置拼写和类型错误不会被吞掉。
func TestReadDatabaseConfigRejectsUnknownAndWrongTypedFields(t *testing.T) {
	cases := []map[string]interface{}{
		{"type": 1},
		{"type": "mysql", "debug": "true"},
		{"type": "mysql", "max_open_conns": 1.5},
		{"type": "mysql", "max_idle_conns": -1},
		{"type": "mysql", "unknown_option": true},
		{"type": "mysql", "params": map[string]interface{}{"timeout": 10}},
	}
	for index, config := range cases {
		if _, err := readDatabaseConfig(config); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
			t.Fatalf("第 %d 个非法配置应返回 ErrInvalidDatabaseConfig，实际为 %v", index, err)
		}
	}
}

// TestApplyDatabaseEnvOverridesRejectsInvalidInteger 验证非法环境变量不会回退并继续启动。
func TestApplyDatabaseEnvOverridesRejectsInvalidInteger(t *testing.T) {
	t.Setenv("DB_MAX_OPEN_CONNS", "1.5")
	app := &App{Env: env.NewEnv()}
	config := db.Config{MaxOpenConns: 10}
	if err := applyDatabaseEnvOverrides(app, &config); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("非法环境整数应返回 ErrInvalidDatabaseConfig，实际为 %v", err)
	}
	if config.MaxOpenConns != 10 {
		t.Fatalf("失败覆盖不得修改原值，实际为 %d", config.MaxOpenConns)
	}
}

// TestAppDoesNotPromoteNonDefaultConnection 验证默认连接缺失时不会选择第一个成功连接。
func TestAppDoesNotPromoteNonDefaultConnection(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	connectorName := fmt.Sprintf("app_default_hardening_%d", time.Now().UnixNano())
	if err := db.RegisterConnector(connectorName, &initTestConnector{}); err != nil {
		t.Fatalf("注册测试连接器失败: %v", err)
	}
	configuration := fmt.Sprintf(`{
  "default": "primary",
  "connections": {
    "analytics": {"type": %q, "database": "analytics"}
  }
}`, connectorName)
	if err := os.WriteFile(filepath.Join(basePath, "config", "database.json"), []byte(configuration), 0o600); err != nil {
		t.Fatalf("写入数据库配置失败: %v", err)
	}
	app := NewApp(basePath)
	t.Cleanup(func() { _ = app.Close() })
	if app.DB != nil {
		t.Fatal("非默认连接不得被提升为 app.DB")
	}
	if startupErr := app.StartupError(); startupErr == nil || !strings.Contains(startupErr.Error(), "primary") {
		t.Fatalf("默认连接缺失应进入启动错误，实际为 %v", startupErr)
	}
}

// failingTestConnector 始终返回连接错误的测试连接器，模拟数据库不可用场景。
type failingTestConnector struct {
	err error
}

func (c *failingTestConnector) Connect(_ db.Config) (db.Connection, error) {
	return nil, c.err
}

// TestDatabaseConnectionFailureDoesNotBlockStartup 验证数据库连接失败（网络不通等运行环境问题）
// 不会阻塞 Web 框架启动。app.DB 应为 nil，StartupError 应为 nil。
func TestDatabaseConnectionFailureDoesNotBlockStartup(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)

	// 注册一个始终连接失败的连接器
	connectorName := fmt.Sprintf("fail_connect_%d", time.Now().UnixNano())
	connectErr := errors.New("dial tcp 127.0.0.1:3306: connection refused")
	if err := db.RegisterConnector(connectorName, &failingTestConnector{err: connectErr}); err != nil {
		t.Fatalf("注册失败连接器失败: %v", err)
	}

	// 写入使用该连接器的数据库配置
	configuration := fmt.Sprintf(`{
  "default": "mysql",
  "connections": {
    "mysql": {"type": %q, "database": "test_db"}
  }
}`, connectorName)
	if err := os.WriteFile(filepath.Join(basePath, "config", "database.json"), []byte(configuration), 0o600); err != nil {
		t.Fatalf("写入数据库配置失败: %v", err)
	}

	app := NewApp(basePath)
	t.Cleanup(func() { _ = app.Close() })

	// 核心断言：连接失败不应产生致命启动错误
	if startupErr := app.StartupError(); startupErr != nil {
		t.Fatalf("数据库连接失败不应产生致命启动错误，实际为: %v", startupErr)
	}

	// app.DB 应为 nil（默认连接未建立），但 DBManager 应已创建
	if app.DB != nil {
		t.Fatal("连接失败时 app.DB 应为 nil")
	}
	if app.DBManager == nil {
		t.Fatal("即使连接失败，DBManager 仍应被创建")
	}
}

// TestDatabaseConfigStructuralErrorStillBlocks 验证数据库配置的结构性错误
// （如非法字段名）仍然是致命启动错误，不会被连接容错逻辑误放行。
func TestDatabaseConfigStructuralErrorStillBlocks(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)

	// 写入包含非法字段的数据库配置
	configuration := `{
  "default": "mysql",
  "connections": {
    "mysql": {"type": "mysql", "database": "test_db", "unknown_field": "bad"}
  }
}`
	if err := os.WriteFile(filepath.Join(basePath, "config", "database.json"), []byte(configuration), 0o600); err != nil {
		t.Fatalf("写入数据库配置失败: %v", err)
	}

	app := NewApp(basePath)
	t.Cleanup(func() { _ = app.Close() })

	// 配置结构性错误应仍然产生致命启动错误
	startupErr := app.StartupError()
	if startupErr == nil {
		t.Fatal("数据库配置结构性错误应产生致命启动错误")
	}
	if !strings.Contains(startupErr.Error(), "unknown_field") {
		t.Fatalf("启动错误应包含非法字段名，实际为: %v", startupErr)
	}
}

