package framework

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

// TestAppStartsWithoutDatabaseConfiguration 验证数据库配置完全缺失时，
// 应用仍可启动并真实使用非数据库模块。
func TestAppStartsWithoutDatabaseConfiguration(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	if err := os.Remove(filepath.Join(basePath, "config", "database.json")); err != nil {
		t.Fatalf("删除数据库配置失败: %v", err)
	}

	app := mustBuildTestApp(t, basePath)
	if app == nil {
		t.Fatal("缺少数据库配置时应用仍应保持可用")
	}
	t.Cleanup(func() { _ = app.Close() })

	if startupErr := app.StartupError(); startupErr != nil {
		t.Fatalf("缺少数据库配置不应产生启动错误，实际为 %v", startupErr)
	}
	if app.db != nil {
		t.Fatal("缺少数据库配置时不应创建默认数据库连接")
	}
	if app.dbManager == nil {
		t.Fatal("缺少数据库配置时仍应创建数据库管理器")
	}
	if connection, err := app.dbManager.Default(); connection != nil || !errors.Is(err, db.ErrConnectionNotFound) {
		t.Fatalf("查询空管理器的默认连接应返回连接不存在，connection=%v err=%v", connection, err)
	}
	if connection, err := app.dbManager.Default(); connection != nil || !errors.Is(err, db.ErrConnectionNotFound) {
		t.Fatalf("重复查询不得制造默认连接，connection=%v err=%v", connection, err)
	}

	const cacheKey = "database_optional_startup"
	const cacheValue = "available"
	if err := app.cache.Set(cacheKey, cacheValue, 0); err != nil {
		t.Fatalf("缺少数据库配置时写入缓存失败: %v", err)
	}
	value, found, err := app.cache.Get(cacheKey)
	if err != nil {
		t.Fatalf("缺少数据库配置时读取缓存失败: %v", err)
	}
	if !found || value != cacheValue {
		t.Fatalf("缓存读写结果错误，found=%v value=%#v", found, value)
	}
}

// TestExplicitDatabasePoliciesHandleMissingConfiguration 验证显式数据库策略不会把缺失配置误判为可用依赖。
func TestExplicitDatabasePoliciesHandleMissingConfiguration(t *testing.T) {
	for _, test := range []struct {
		name             string
		policy           DatabaseStartupPolicy
		wantStartupError bool
		wantReady        bool
	}{
		{name: "required", policy: DatabaseStartupRequired, wantStartupError: true, wantReady: false},
		{name: "degraded", policy: DatabaseStartupDegraded, wantStartupError: false, wantReady: false},
		{name: "disabled", policy: DatabaseStartupDisabled, wantStartupError: false, wantReady: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			basePath := t.TempDir()
			writeTestAppConfigFiles(t, basePath)
			if err := os.Remove(filepath.Join(basePath, "config", "database.json")); err != nil {
				t.Fatalf("删除数据库配置失败: %v", err)
			}
			setDatabaseStartupPolicyForTest(t, basePath, test.policy)

			app, _ := initializeTestApp(t, basePath)
			startupErr := app.StartupError()
			if test.wantStartupError != (startupErr != nil) {
				t.Fatalf("策略 %s 的启动错误状态不符: %v", test.policy, startupErr)
			}
			if test.wantStartupError && !errors.Is(startupErr, db.ErrDatabaseUnavailable) {
				t.Fatalf("required 缺失配置应保留数据库不可用根因: %v", startupErr)
			}
			report := app.health.Readiness(context.Background())
			if report.Healthy() != test.wantReady {
				t.Fatalf("策略 %s 的 readiness 状态不符: %#v", test.policy, report)
			}
		})
	}
}

func setDatabaseStartupPolicyForTest(t *testing.T, basePath string, policy DatabaseStartupPolicy) {
	t.Helper()
	appConfigPath := filepath.Join(basePath, "config", "app.json")
	appConfig, err := os.ReadFile(appConfigPath)
	if err != nil {
		t.Fatalf("读取应用配置失败: %v", err)
	}
	configured := strings.Replace(string(appConfig), `"app_env": "test",`, `"app_env": "test",`+"\n  \"database_startup_policy\": \""+string(policy)+"\",", 1)
	if err = os.WriteFile(appConfigPath, []byte(configured), 0o600); err != nil {
		t.Fatalf("写入数据库启动策略失败: %v", err)
	}
}

// TestDatabaseUnavailableDriverHardening 验证显式有效配置引用未注册驱动时，
// 框架只记录连接警告，不产生致命启动错误。
func TestDatabaseUnavailableDriverHardening(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	setDatabaseStartupPolicyForTest(t, basePath, DatabaseStartupDegraded)
	driverName := fmt.Sprintf("unregistered_driver_%d", time.Now().UnixNano())
	configuration := fmt.Sprintf(`{
  "default": "primary",
  "connections": {
    "primary": {"type": %q, "database": "test_db"}
  }
}`, driverName)
	if err := os.WriteFile(filepath.Join(basePath, "config", "database.json"), []byte(configuration), 0o600); err != nil {
		t.Fatalf("写入未注册驱动配置失败: %v", err)
	}

	app := mustBuildTestApp(t, basePath)
	t.Cleanup(func() { _ = app.Close() })
	if startupErr := app.StartupError(); startupErr != nil {
		t.Fatalf("未注册数据库驱动不应阻塞应用启动，实际为 %v", startupErr)
	}
	if app.db != nil {
		t.Fatal("未注册数据库驱动不应产生默认连接")
	}
	if app.dbManager == nil {
		t.Fatal("连接初始化失败时仍应保留数据库管理器")
	}
	if connection, err := app.dbManager.Default(); connection != nil || !errors.Is(err, db.ErrConnectorNotFound) {
		t.Fatalf("降级连接重试应保留驱动缺失的实际原因，connection=%v err=%v", connection, err)
	}

	const cacheKey = "unregistered_driver_startup"
	if err := app.cache.Set(cacheKey, "available", 0); err != nil {
		t.Fatalf("数据库驱动不可用时写入缓存失败: %v", err)
	}
	if value, found, err := app.cache.Get(cacheKey); err != nil || !found || value != "available" {
		t.Fatalf("数据库驱动不可用时缓存读写结果错误，found=%v value=%#v err=%v", found, value, err)
	}
	if err := app.log.Flush(context.Background()); err != nil {
		t.Fatalf("刷新连接初始化警告失败: %v", err)
	}
	logs := readDatabaseHardeningLogs(t, filepath.Join(basePath, "runtime", "log"))
	if !strings.Contains(logs, "[WARNING]") || !strings.Contains(logs, driverName) || !strings.Contains(logs, "应用仍可启动") {
		t.Fatalf("未注册驱动应记录可恢复的连接警告，实际日志为 %q", logs)
	}
}

// TestDatabaseExplicitConfigurationHardening 验证显式存在的错误数据库配置
// 仍按严格模式记录致命启动错误。
func TestDatabaseExplicitConfigurationHardening(t *testing.T) {
	validConnection := fmt.Sprintf(`{"primary":{"type":%q,"database":"test"}}`, appInitConnectorName)
	tests := []struct {
		name          string
		configuration string
		errorText     string
	}{
		{name: "空对象", configuration: `{}`, errorText: "database 配置不能为空"},
		{name: "空默认连接", configuration: `{"default":"","connections":` + validConnection + `}`, errorText: "database.default"},
		{name: "默认连接类型错误", configuration: `{"default":1,"connections":` + validConnection + `}`, errorText: "database.default"},
		{name: "连接定义缺失", configuration: `{"default":"primary"}`, errorText: "database.connections"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			basePath := t.TempDir()
			writeTestAppConfigFiles(t, basePath)
			if err := os.WriteFile(filepath.Join(basePath, "config", "database.json"), []byte(test.configuration), 0o600); err != nil {
				t.Fatalf("写入错误数据库配置失败: %v", err)
			}

			app, _ := initializeTestApp(t, basePath)
			startupErr := app.StartupError()
			if startupErr == nil || !errors.Is(startupErr, db.ErrInvalidDatabaseConfig) {
				t.Fatalf("显式错误数据库配置应产生 ErrInvalidDatabaseConfig，实际为 %v", startupErr)
			}
			if !strings.Contains(startupErr.Error(), test.errorText) {
				t.Fatalf("启动错误应包含 %q，实际为 %v", test.errorText, startupErr)
			}
		})
	}
}

func readDatabaseHardeningLogs(t *testing.T, directory string) string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("读取应用日志目录失败: %v", err)
	}
	var content strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".log" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			t.Fatalf("读取应用日志 %s 失败: %v", entry.Name(), readErr)
		}
		content.Write(data)
	}
	return content.String()
}

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
	app, _ := initializeTestApp(t, basePath)
	if app.db != nil {
		t.Fatal("非默认连接不得被提升为 ServiceDB")
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
// 不会阻塞 Web 框架启动。ServiceDB 应不可用，StartupError 应为 nil。
func TestDatabaseConnectionFailureDoesNotBlockStartup(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	setDatabaseStartupPolicyForTest(t, basePath, DatabaseStartupDegraded)

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

	app, _ := initializeTestApp(t, basePath)

	// 核心断言：连接失败不应产生致命启动错误
	if startupErr := app.StartupError(); startupErr != nil {
		t.Fatalf("数据库连接失败不应产生致命启动错误，实际为: %v", startupErr)
	}

	// ServiceDB 应不可用（默认连接未建立），但 ServiceDBManager 应已创建
	if app.db != nil {
		t.Fatal("连接失败时 ServiceDB 应不可用")
	}
	if app.dbManager == nil {
		t.Fatal("即使连接失败，ServiceDBManager 仍应被创建")
	}
}

// TestRequiredDatabaseConnectionFailureBlocksStartup 验证 required 策略把默认数据库故障升级为启动错误。
func TestRequiredDatabaseConnectionFailureBlocksStartup(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	appConfigPath := filepath.Join(basePath, "config", "app.json")
	appConfig, err := os.ReadFile(appConfigPath)
	if err != nil {
		t.Fatalf("读取应用配置失败: %v", err)
	}
	configured := strings.Replace(string(appConfig), `"app_env": "test",`, `"app_env": "test",`+"\n  \"database_startup_policy\": \"required\",", 1)
	if err = os.WriteFile(appConfigPath, []byte(configured), 0o600); err != nil {
		t.Fatalf("写入 required 数据库策略失败: %v", err)
	}

	connectorName := fmt.Sprintf("required_fail_connect_%d", time.Now().UnixNano())
	connectErr := errors.New("required database unavailable")
	if err = db.RegisterConnector(connectorName, &failingTestConnector{err: connectErr}); err != nil {
		t.Fatalf("注册 required 失败连接器失败: %v", err)
	}
	databaseConfig := fmt.Sprintf(`{
  "default": "mysql",
  "connections": {
    "mysql": {"type": %q, "database": "test_db"}
  }
}`, connectorName)
	if err = os.WriteFile(filepath.Join(basePath, "config", "database.json"), []byte(databaseConfig), 0o600); err != nil {
		t.Fatalf("写入 required 数据库配置失败: %v", err)
	}

	app, _ := initializeTestApp(t, basePath)
	if startupErr := app.StartupError(); !errors.Is(startupErr, connectErr) {
		t.Fatalf("required 默认数据库故障必须阻断启动: %v", startupErr)
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

	app, _ := initializeTestApp(t, basePath)

	// 配置结构性错误应仍然产生致命启动错误
	startupErr := app.StartupError()
	if startupErr == nil {
		t.Fatal("数据库配置结构性错误应产生致命启动错误")
	}
	if !strings.Contains(startupErr.Error(), "unknown_field") {
		t.Fatalf("启动错误应包含非法字段名，实际为: %v", startupErr)
	}
}
