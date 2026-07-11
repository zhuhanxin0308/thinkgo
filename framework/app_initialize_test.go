package framework

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"thinkgo/framework/db"
)

// TestNormalizeViewPathHandlesEmptyValue 验证空视图路径不会导致初始化阶段 panic。
func TestNormalizeViewPathHandlesEmptyValue(t *testing.T) {
	viewConfig := map[string]interface{}{
		"view_path": "",
	}

	normalizeViewPath("C:/workspace/project", viewConfig)

	if got := viewConfig["view_path"]; got != "" {
		t.Fatalf("空视图路径应保持为空，实际为 %v", got)
	}
}

// TestNormalizeViewPathMakesRelativePathAbsolute 验证相对视图目录会被归一化到项目根目录下。
func TestNormalizeViewPathMakesRelativePathAbsolute(t *testing.T) {
	viewConfig := map[string]interface{}{
		"view_path": "app/view",
	}

	normalizeViewPath("C:/workspace/project", viewConfig)

	if got := viewConfig["view_path"]; got != "C:/workspace/project/app/view" {
		t.Fatalf("相对视图路径归一化错误，实际为 %v", got)
	}
}

type perRequestTestController struct {
	marker int
}

// initTestConnection 用最小连接实现隔离初始化测试，避免依赖真实数据库驱动。
type initTestConnection struct{}

func (c *initTestConnection) Select(table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	return nil, nil
}

func (c *initTestConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	return 1, nil
}

func (c *initTestConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	return 1, nil
}

func (c *initTestConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	return 1, nil
}

func (c *initTestConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *initTestConnection) Close() error {
	return nil
}

// initTestConnector 记录应用初始化时传入连接器的配置，验证数据库配置是否被正确加载。
type initTestConnector struct {
	lock   sync.Mutex
	config db.Config
	called bool
}

func (c *initTestConnector) Connect(config db.Config) (db.Connection, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.config = config
	c.called = true
	return &initTestConnection{}, nil
}

func (c *initTestConnector) LastConfig() (db.Config, bool) {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.config, c.called
}

// writeTestAppConfigFiles 写入初始化框架所需的最小配置文件。
func writeTestAppConfigFiles(t *testing.T, basePath string) {
	t.Helper()

	configDir := filepath.Join(basePath, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("创建测试配置目录失败: %v", err)
	}

	files := map[string]string{
		"app.json": `{
  "app_debug": false,
  "app_trace": false,
  "default_lang": "zh-cn",
  "session_enable": false,
  "server": {
    "host": "127.0.0.1",
    "port": 8080,
    "read_header_timeout_ms": 1000,
    "read_timeout_ms": 2000,
    "write_timeout_ms": 2000,
    "idle_timeout_ms": 3000,
    "shutdown_timeout_ms": 1000,
    "max_header_bytes": 1048576,
    "max_body_bytes": 1048576,
    "multipart_max_memory_mb": 8
  },
  "compression": {
    "enable": false
  }
}`,
		"log.json": `{
  "default": "file",
  "channels": {
    "file": {
      "type": "file",
      "path": "` + filepath.ToSlash(filepath.Join(basePath, "runtime", "log")) + `"
    }
  }
}`,
		"cache.json": `{
  "default": "file",
  "stores": {
    "file": {
      "type": "file",
      "path": "` + filepath.ToSlash(filepath.Join(basePath, "runtime", "cache")) + `"
    }
  }
}`,
		"view.json": `{
  "view_path": "app/view",
  "view_suffix": "html",
  "view_depr": "/"
}`,
		"cookie.json": `{}`,
		"session.json": `{
  "type": "memory",
  "name": "TESTSESSID",
  "expire": 600
}`,
		"database.json": `{
  "default": "sqlite",
  "connections": {
    "sqlite": {
      "type": "sqlite",
      "database": ":memory:"
    }
  }
}`,
	}

	for name, content := range files {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写入测试配置 %s 失败: %v", name, err)
		}
	}
}

// TestInitializeReportsLanguageLoadError 验证应用初始化不会吞掉语言包加载错误。
func TestInitializeReportsLanguageLoadError(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	langDir := filepath.Join(basePath, "app", "lang")
	if err := os.MkdirAll(langDir, 0o755); err != nil {
		t.Fatalf("创建语言目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(langDir, "zh-cn.json"), []byte(`{"auth":`), 0o644); err != nil {
		t.Fatalf("写入损坏语言包失败: %v", err)
	}

	app := NewApp(basePath)
	defer func() {
		if app.Log != nil {
			app.Log.Shutdown()
		}
		if app.DB != nil {
			_ = app.DB.Close()
		}
	}()

	err := app.StartupError()
	if err == nil {
		t.Fatal("损坏语言包应记录启动错误")
	}
	if !strings.Contains(err.Error(), "load language files failed") {
		t.Fatalf("启动错误应指向语言包加载失败，实际为 %v", err)
	}
}

// TestInitializeBindsControllersAsFactory 验证初始化后控制器应按请求新建，而不是被容器缓存成单例。
func TestInitializeBindsControllersAsFactory(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)

	const controllerName = "__test_per_request_controller__"
	RegisterController(controllerName, &perRequestTestController{})
	defer delete(ControllerRegistry, controllerName)

	app := NewApp(basePath)
	defer func() {
		if app.Log != nil {
			app.Log.Shutdown()
		}
		if app.DB != nil {
			_ = app.DB.Close()
		}
	}()

	first, err := app.Make(controllerName)
	if err != nil {
		t.Fatalf("第一次解析控制器失败: %v", err)
	}
	second, err := app.Make(controllerName)
	if err != nil {
		t.Fatalf("第二次解析控制器失败: %v", err)
	}

	if first == second {
		t.Fatal("控制器应为每次请求创建新实例，不应被容器缓存")
	}
}

// TestInitializeLoadsUnixTimestampValueType 验证应用初始化会把数据库配置中的 Unix 时间戳模式传给连接器，
// 避免运行时仍回退到 datetime 模式而导致 int 时间字段更新失败。
func TestInitializeLoadsUnixTimestampValueType(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)

	connectorName := "__test_init_timestamp_connector__"
	connector := &initTestConnector{}
	db.RegisterConnector(connectorName, connector)

	databaseConfig := fmt.Sprintf(`{
  "default": %q,
  "connections": {
    %q: {
      "type": %q,
      "database": "ignored",
      "auto_timestamp": true,
      "create_time_field": "create_time",
      "update_time_field": "update_time",
      "timestamp_value_type": "unix"
    }
  }
}`, connectorName, connectorName, connectorName)
	if err := os.WriteFile(filepath.Join(basePath, "config", "database.json"), []byte(databaseConfig), 0o644); err != nil {
		t.Fatalf("覆盖数据库配置失败: %v", err)
	}

	app := NewApp(basePath)
	defer func() {
		if app.Log != nil {
			app.Log.Shutdown()
		}
		if app.DB != nil {
			_ = app.DB.Close()
		}
	}()

	if app.DB == nil {
		t.Fatal("应用初始化后默认数据库连接不应为空")
	}

	config, ok := connector.LastConfig()
	if !ok {
		t.Fatal("初始化阶段应调用测试连接器")
	}
	if config.TimestampValueType != db.TimestampValueTypeUnix {
		t.Fatalf("初始化后传给连接器的时间戳模式错误，实际为 %q", config.TimestampValueType)
	}
	if !config.AutoTimestamp {
		t.Fatal("初始化后应保留 auto_timestamp 配置")
	}
	if config.CreateTimeField != "create_time" || config.UpdateTimeField != "update_time" {
		t.Fatalf("初始化后时间字段配置错误，实际 create=%q update=%q", config.CreateTimeField, config.UpdateTimeField)
	}
}
