package framework

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"thinkgo/framework/cache"
	"thinkgo/framework/db"
	frameworkenv "thinkgo/framework/env"
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

// TestApplicationURLHelpersNormalizeAndValidateInputs 验证 URL 拼接不会产生双斜杠、协议绕过或控制字符注入。
func TestApplicationURLHelpersNormalizeAndValidateInputs(t *testing.T) {
	t.Setenv("SERVER_DOMAIN", "https://example.com/")
	app := &App{DebugMode: true, Env: frameworkenv.NewEnv()}
	if !app.IsDebug() {
		t.Fatal("IsDebug 应返回应用调试状态")
	}
	if got := app.Domain(); got != "https://example.com/" {
		t.Fatalf("Domain 读取错误，实际为 %q", got)
	}
	if got := app.URL("/assets/app.js"); got != "https://example.com/assets/app.js" {
		t.Fatalf("相对 URL 归一化错误，实际为 %q", got)
	}
	if got := app.AssetURL("assets/app.css"); got != "https://example.com/assets/app.css" {
		t.Fatalf("AssetURL 归一化错误，实际为 %q", got)
	}
	if got := app.URL("HTTP://cdn.example.com/app.js"); got != "HTTP://cdn.example.com/app.js" {
		t.Fatalf("大小写不同的完整 HTTP URL 应保持不变，实际为 %q", got)
	}
	if got := app.URL("https://"); got != "" {
		t.Fatalf("缺少 host 的完整 URL 应被拒绝，实际为 %q", got)
	}
	if got := app.URL("/safe\r\nX-Test: injected"); got != "" {
		t.Fatalf("包含控制字符的 URL 应被拒绝，实际为 %q", got)
	}
}

// TestReadConfigIntValueUsesStrictNonnegativeIntegerSemantics 验证连接池配置不截断小数或溢出整数。
func TestReadConfigIntValueUsesStrictNonnegativeIntegerSemantics(t *testing.T) {
	validCases := map[interface{}]int{
		int(0):             0,
		int(8):             8,
		int64(16):          16,
		float64(32):        32,
		json.Number("128"): 128,
		uint64(256):        256,
	}
	for raw, expected := range validCases {
		got, err := readConfigIntValue(raw)
		if err != nil || got != expected {
			t.Fatalf("合法整数 %#v 解析错误，期望 %d，实际 %d，错误 %v", raw, expected, got, err)
		}
	}
	invalidCases := []interface{}{
		-1,
		int64(-1),
		1.5,
		"64",
		true,
		json.Number("1.5"),
		uint64(maxIntValue()) + 1,
	}
	for _, raw := range invalidCases {
		if got, err := readConfigIntValue(raw); err == nil {
			t.Fatalf("非法连接池整数 %#v 应返回错误，实际值为 %d", raw, got)
		}
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

var (
	appInitConnectorOnce sync.Once
	appInitConnectorErr  error
)

const appInitConnectorName = "__app_init_test_connector__"

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
	appInitConnectorOnce.Do(func() {
		appInitConnectorErr = db.RegisterConnector(appInitConnectorName, &initTestConnector{})
	})
	if appInitConnectorErr != nil {
		t.Fatalf("注册应用初始化测试连接器失败: %v", appInitConnectorErr)
	}

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
  "cache": true
}`,
		"cookie.json": `{}`,
		"session.json": `{
  "type": "memory",
  "name": "TESTSESSID",
  "expire": 600
}`,
		"database.json": `{
  "default": "` + appInitConnectorName + `",
  "connections": {
    "` + appInitConnectorName + `": {
      "type": "` + appInitConnectorName + `",
      "database": "test"
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

// TestCreateAppCacheRegistersDefaultAliasAndNamedStores 验证默认驱动同时可通过配置名称访问，且不同 store 相互隔离。
func TestCreateAppCacheRegistersDefaultAliasAndNamedStores(t *testing.T) {
	basePath := t.TempDir()
	app := &App{BasePath: basePath}
	manager, err := createAppCache(app, map[string]interface{}{
		"default": "file",
		"stores": map[string]interface{}{
			"file": map[string]interface{}{
				"type": "file",
				"path": "./runtime/cache",
			},
			"memory": map[string]interface{}{
				"type": "memory",
			},
		},
	})
	if err != nil {
		t.Fatalf("创建多 store 缓存失败: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	fileStore, err := manager.Store("file")
	if err != nil {
		t.Fatalf("默认 file store 未按名称注册: %v", err)
	}
	memoryStore, err := manager.Store("memory")
	if err != nil {
		t.Fatalf("memory store 未注册: %v", err)
	}
	if err = manager.Set("shared", "file-value", 0); err != nil {
		t.Fatalf("写入默认缓存失败: %v", err)
	}
	if value, found, getErr := fileStore.Get("shared"); getErr != nil || !found || value != "file-value" {
		t.Fatalf("默认别名与 file store 未共享驱动: value=%#v found=%t err=%v", value, found, getErr)
	}
	if _, found, getErr := memoryStore.Get("shared"); getErr != nil || found {
		t.Fatalf("memory store 不应读到 file 数据: found=%t err=%v", found, getErr)
	}
	if _, statErr := os.Stat(filepath.Join(basePath, "runtime", "cache")); statErr != nil {
		t.Fatalf("相对文件缓存目录未归一化到应用根目录: %v", statErr)
	}
}

// TestCreateAppCacheRejectsInvalidConfiguration 验证缓存配置错误在创建任何隐式回退驱动前明确失败。
func TestCreateAppCacheRejectsInvalidConfiguration(t *testing.T) {
	basePath := t.TempDir()
	validFileStore := map[string]interface{}{
		"file": map[string]interface{}{"type": "file", "path": "./runtime/cache"},
	}
	testCases := map[string]map[string]interface{}{
		"未知顶层字段": {
			"default": "file", "stores": validFileStore, "unexpected": true,
		},
		"默认 store 不存在": {
			"default": "missing", "stores": validFileStore,
		},
		"废弃 expire 字段": {
			"default": "file",
			"stores": map[string]interface{}{
				"file": map[string]interface{}{"type": "file", "path": "./runtime/cache", "expire": float64(60)},
			},
		},
		"相对路径越界": {
			"default": "file",
			"stores": map[string]interface{}{
				"file": map[string]interface{}{"type": "file", "path": "../outside"},
			},
		},
		"路径控制字符": {
			"default": "file",
			"stores": map[string]interface{}{
				"file": map[string]interface{}{"type": "file", "path": "runtime/\tcache"},
			},
		},
		"Redis 类型错误": {
			"default": "redis",
			"stores": map[string]interface{}{
				"redis": map[string]interface{}{"type": "redis", "timeout_ms": "slow"},
			},
		},
		"不支持的驱动": {
			"default": "unknown",
			"stores": map[string]interface{}{
				"unknown": map[string]interface{}{"type": "unknown"},
			},
		},
	}
	for name, cacheConfig := range testCases {
		t.Run(name, func(t *testing.T) {
			manager, err := createAppCache(&App{BasePath: basePath}, cacheConfig)
			if err == nil || manager != nil {
				t.Fatalf("非法配置应失败且不返回管理器: manager=%#v err=%v", manager, err)
			}
		})
	}
}

// TestInitializeDoesNotFallbackFromInvalidCacheConfig 验证应用启动不会把非法缓存配置静默替换成文件驱动。
func TestInitializeDoesNotFallbackFromInvalidCacheConfig(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	invalidCache := `{
  "default": "file",
  "stores": {
    "file": {"type": "file", "path": "./runtime/cache", "expire": 60}
  }
}`
	if err := os.WriteFile(filepath.Join(basePath, "config", "cache.json"), []byte(invalidCache), 0o600); err != nil {
		t.Fatalf("写入非法缓存配置失败: %v", err)
	}
	app := NewConsoleApp(basePath)
	t.Cleanup(func() { _ = app.Close() })
	if startupErr := app.StartupError(); startupErr == nil || !strings.Contains(startupErr.Error(), "初始化缓存失败") {
		t.Fatalf("非法缓存配置未进入启动错误: %v", startupErr)
	}
	if _, _, err := app.Cache.Get("key"); !errors.Is(err, cache.ErrCacheDriverNotConfigured) {
		t.Fatalf("非法配置后不应安装隐式回退驱动，实际为 %v", err)
	}
}

// TestInitializeBuildsStrictCookieSessionAndCSRFServices 验证安全模块配置、路径和容器绑定统一生效。
func TestInitializeBuildsStrictCookieSessionAndCSRFServices(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	secret := strings.Repeat("s", 32)
	cookieConfig := `{
  "prefix": "think_",
  "secret": "` + secret + `",
  "path": "/",
  "httponly": true,
  "samesite": "Lax",
  "sign_max_age": 3600
}`
	sessionConfig := `{
  "name": "SID",
  "type": "file",
  "storage_path": "./runtime/session",
  "cookie_path": "/account",
  "expire": 600,
  "httponly": true,
  "samesite": "Strict",
  "max_data_bytes": 32768
}`
	csrfConfig := `{
  "cookie_name": "csrf_token",
  "header_name": "X-CSRF-Token",
  "field_name": "_csrf",
  "cookie_path": "/",
  "secure": false,
  "samesite": "Lax",
  "max_age": 600,
  "secret": "",
  "safe_methods": ["GET", "HEAD", "OPTIONS"]
}`
	for name, content := range map[string]string{
		"cookie.json": cookieConfig, "session.json": sessionConfig, "csrf.json": csrfConfig,
	} {
		if err := os.WriteFile(filepath.Join(basePath, "config", name), []byte(content), 0o600); err != nil {
			t.Fatalf("写入安全模块测试配置 %s 失败: %v", name, err)
		}
	}
	app := NewConsoleApp(basePath)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.StartupError(); err != nil {
		t.Fatalf("合法安全模块配置不应产生启动错误: %v", err)
	}
	if app.Cookie == nil || app.Session == nil || app.Middleware.ResolveAlias("csrf") == nil {
		t.Fatal("Cookie、Session 与 CSRF 服务必须全部初始化")
	}
	if app.Get("cookie") != app.Cookie || app.Get("session") != app.Session {
		t.Fatal("Cookie 与 Session 必须绑定到应用容器")
	}
	expectedStorage := filepath.Clean(filepath.Join(basePath, "runtime", "session"))
	if app.Session.GetConfig().StoragePath != expectedStorage {
		t.Fatalf("Session 相对路径未基于应用根目录解析: %q", app.Session.GetConfig().StoragePath)
	}
	if _, err := os.Stat(expectedStorage); err != nil {
		t.Fatalf("Session 存储目录未创建: %v", err)
	}
}

// TestInitializeReportsInvalidSecurityModuleConfiguration 验证安全配置错误不会被隐式默认值掩盖。
func TestInitializeReportsInvalidSecurityModuleConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		filename   string
		content    string
		errorMatch string
	}{
		{name: "Cookie 未知字段", filename: "cookie.json", content: `{"unexpected":true}`, errorMatch: "初始化 Cookie 失败"},
		{name: "Session 废弃 path 字段", filename: "session.json", content: `{"type":"memory","path":"./runtime/session"}`, errorMatch: "初始化 Session 失败"},
		{name: "Session 相对路径越界", filename: "session.json", content: `{"type":"file","storage_path":"../outside"}`, errorMatch: "初始化 Session 失败"},
		{name: "CSRF 把 POST 设为安全方法", filename: "csrf.json", content: `{"safe_methods":["GET","POST"]}`, errorMatch: "初始化 CSRF 失败"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			basePath := t.TempDir()
			writeTestAppConfigFiles(t, basePath)
			if err := os.WriteFile(filepath.Join(basePath, "config", test.filename), []byte(test.content), 0o600); err != nil {
				t.Fatalf("写入非法安全配置失败: %v", err)
			}
			app := NewConsoleApp(basePath)
			t.Cleanup(func() { _ = app.Close() })
			if err := app.StartupError(); err == nil || !strings.Contains(err.Error(), test.errorMatch) {
				t.Fatalf("非法配置应进入启动错误并包含 %q，实际为 %v", test.errorMatch, err)
			}
			if app.Cookie == nil || app.Session == nil || app.Middleware.ResolveAlias("csrf") == nil {
				t.Fatal("启动错误后安全服务仍必须保持非空且可拒绝请求")
			}
		})
	}
}

// TestInitializeReportsEnvironmentLoadError 验证损坏的 .env 不会被初始化流程静默忽略。
func TestInitializeReportsEnvironmentLoadError(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	if err := os.WriteFile(filepath.Join(basePath, ".env"), []byte("INVALID LINE"), 0o600); err != nil {
		t.Fatalf("写入损坏环境文件失败: %v", err)
	}

	app := NewConsoleApp(basePath)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.StartupError(); err == nil || !strings.Contains(err.Error(), "load environment failed") {
		t.Fatalf("损坏 .env 应进入启动错误，实际为 %v", err)
	}
}

// TestInitializeRejectsInvalidBooleanEnvironmentOverride 验证非法布尔环境变量不会被静默解释为 false。
func TestInitializeRejectsInvalidBooleanEnvironmentOverride(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	t.Setenv("APP_DEBUG", "not-a-boolean")

	app := NewConsoleApp(basePath)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.StartupError(); err == nil || !strings.Contains(err.Error(), "APP_DEBUG") {
		t.Fatalf("非法 APP_DEBUG 应进入启动错误，实际为 %v", err)
	}
}

// TestEnvironmentUsesUnifiedEnvironmentPrecedence 验证 Environment 同样遵循进程变量优先、.env 兜底。
func TestEnvironmentUsesUnifiedEnvironmentPrecedence(t *testing.T) {
	previous, existed := os.LookupEnv("APP_ENV")
	if err := os.Unsetenv("APP_ENV"); err != nil {
		t.Fatalf("清理 APP_ENV 失败: %v", err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("APP_ENV", previous)
		} else {
			_ = os.Unsetenv("APP_ENV")
		}
	})

	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	if err := os.WriteFile(filepath.Join(basePath, ".env"), []byte("APP_ENV=staging\n"), 0o600); err != nil {
		t.Fatalf("写入环境文件失败: %v", err)
	}
	app := NewConsoleApp(basePath)
	t.Cleanup(func() { _ = app.Close() })
	if got := app.Environment(); got != "staging" {
		t.Fatalf("Environment 应读取 .env 兜底，实际为 %q", got)
	}
}

// TestInitializeIsIdempotent 验证重复初始化不会替换资源或叠加后台任务和中间件。
func TestInitializeIsIdempotent(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app := NewConsoleApp(basePath)
	t.Cleanup(func() { _ = app.Close() })

	manager := app.DBManager
	logger := app.Log
	pipeline := app.Middleware
	if err := app.Initialize(); err != nil {
		t.Fatalf("重复初始化应返回首次结果，实际为 %v", err)
	}
	if app.DBManager != manager || app.Log != logger || app.Middleware != pipeline {
		t.Fatal("重复 Initialize 不应替换已初始化资源")
	}
}

// TestInitializeConvertsUnexpectedPanicToStartupError 验证初始化异常不会直接击穿进程。
func TestInitializeConvertsUnexpectedPanicToStartupError(t *testing.T) {
	app := &App{}
	if err := app.Initialize(); !errors.Is(err, ErrApplicationInitializationPanic) {
		t.Fatalf("初始化 panic 应转换为 ErrApplicationInitializationPanic，实际为 %v", err)
	}
	if err := app.Initialize(); !errors.Is(err, ErrApplicationInitializationPanic) {
		t.Fatalf("重复 Initialize 应返回首次 panic 结果，实际为 %v", err)
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
			_ = app.Log.Close()
		}
		if app.DB != nil {
			_ = app.DB.Close()
		}
	}()

	err := app.StartupError()
	if err == nil {
		t.Fatal("损坏语言包应记录启动错误")
	}
	if !strings.Contains(err.Error(), "加载语言文件失败") {
		t.Fatalf("启动错误应指向语言包加载失败，实际为 %v", err)
	}
}

// TestInitializeReportsInvalidLanguageConfig 验证非法多语言配置会阻止应用静默使用默认值启动。
func TestInitializeReportsInvalidLanguageConfig(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	configFile := filepath.Join(basePath, "config", "lang.json")
	if err := os.WriteFile(configFile, []byte(`{"default_lang":"zh-cn","unknown":true}`), 0o644); err != nil {
		t.Fatalf("写入非法多语言配置失败: %v", err)
	}

	app := NewConsoleApp(basePath)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.StartupError(); err == nil || !strings.Contains(err.Error(), "初始化多语言配置失败") {
		t.Fatalf("非法多语言配置应进入启动错误，实际为 %v", err)
	}
}

// TestInitializeBindsControllersAsFactory 验证初始化后控制器应按请求新建，而不是被容器缓存成单例。
func TestInitializeBindsControllersAsFactory(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)

	const controllerName = "__test_per_request_controller__"
	if err := RegisterController(controllerName, &perRequestTestController{}); err != nil {
		t.Fatalf("注册测试控制器失败: %v", err)
	}
	defer unregisterController(controllerName)

	app := NewApp(basePath)
	defer func() {
		if app.Log != nil {
			_ = app.Log.Close()
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

	firstController, firstOK := first.(*perRequestTestController)
	secondController, secondOK := second.(*perRequestTestController)
	if !firstOK || !secondOK {
		t.Fatalf("控制器解析类型错误: first=%T second=%T", first, second)
	}
	firstController.marker = 1
	if firstController == secondController || secondController.marker != 0 {
		t.Fatal("控制器应为每次请求创建新实例，不应被容器缓存")
	}
}

// TestInitializeLoadsUnixTimestampValueType 验证应用初始化会把数据库配置中的 Unix 时间戳模式传给连接器，
// 避免运行时仍回退到 datetime 模式而导致 int 时间字段更新失败。
func TestInitializeLoadsUnixTimestampValueType(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)

	connectorName := fmt.Sprintf("__test_init_timestamp_connector_%d__", time.Now().UnixNano())
	connector := &initTestConnector{}
	if err := db.RegisterConnector(connectorName, connector); err != nil {
		t.Fatalf("注册测试连接器失败: %v", err)
	}

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
			_ = app.Log.Close()
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

// TestInitializeAppliesLogRetentionConfiguration 验证应用配置会完整传递文件数量和容量治理参数。
func TestInitializeAppliesLogRetentionConfiguration(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	logDirectory := filepath.Join(basePath, "runtime", "managed-log")
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}
	now := time.Now()
	for offset := 1; offset <= 2; offset++ {
		filename := filepath.Join(logDirectory, now.AddDate(0, 0, -offset).Format("2006-01-02")+".log")
		if err := os.WriteFile(filename, []byte("old"), 0o600); err != nil {
			t.Fatalf("创建旧日志失败: %v", err)
		}
	}
	logConfig := fmt.Sprintf(`{
  "default": "file",
  "channels": {
    "file": {
      "type": "file",
      "path": %q,
      "max_file_size": 1048576,
      "retention_days": 30,
      "max_files": 1,
      "max_total_size": 1048576
    }
  }
}`, filepath.ToSlash(logDirectory))
	if err := os.WriteFile(filepath.Join(basePath, "config", "log.json"), []byte(logConfig), 0o600); err != nil {
		t.Fatalf("覆盖日志配置失败: %v", err)
	}

	app := NewApp(basePath)
	defer func() {
		if app.Log != nil {
			_ = app.Log.Close()
		}
		if app.DB != nil {
			_ = app.DB.Close()
		}
	}()
	if err := app.StartupError(); err != nil {
		t.Fatalf("应用初始化失败: %v", err)
	}
	app.Log.Info("trigger retention")
	if err := app.Log.Flush(context.Background()); err != nil {
		t.Fatalf("刷盘应用日志失败: %v", err)
	}

	entries, err := os.ReadDir(logDirectory)
	if err != nil {
		t.Fatalf("读取日志目录失败: %v", err)
	}
	logCount := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".log" {
			logCount++
		}
	}
	if logCount != 1 {
		t.Fatalf("max_files=1 时应只保留当前日志，实际为 %d 个", logCount)
	}
}

// TestInitializeReportsInvalidLogDirectory 验证日志目录初始化失败会进入启动错误而非延迟静默失败。
func TestInitializeReportsInvalidLogDirectory(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	invalidPath := filepath.Join(basePath, "runtime", "not-a-directory")
	if err := os.MkdirAll(filepath.Dir(invalidPath), 0o700); err != nil {
		t.Fatalf("创建运行目录失败: %v", err)
	}
	if err := os.WriteFile(invalidPath, []byte("file"), 0o600); err != nil {
		t.Fatalf("创建冲突文件失败: %v", err)
	}
	logConfig := fmt.Sprintf(`{
  "default": "file",
  "channels": {
    "file": {"type": "file", "path": %q}
  }
}`, filepath.ToSlash(invalidPath))
	if err := os.WriteFile(filepath.Join(basePath, "config", "log.json"), []byte(logConfig), 0o600); err != nil {
		t.Fatalf("覆盖日志配置失败: %v", err)
	}

	app := NewApp(basePath)
	defer func() {
		if app.Log != nil {
			_ = app.Log.Close()
		}
		if app.DB != nil {
			_ = app.DB.Close()
		}
	}()
	err := app.StartupError()
	if err == nil || !strings.Contains(err.Error(), "日志目录") {
		t.Fatalf("无效日志目录应产生明确启动错误，实际为 %v", err)
	}
}

// TestReadLogFileOptionsUsesStrictIntegerSemantics 验证日志容量配置拒绝小数、字符串和 int64 溢出。
func TestReadLogFileOptionsUsesStrictIntegerSemantics(t *testing.T) {
	options, err := readLogFileOptions(map[string]interface{}{
		"max_file_size":  int64(1024),
		"retention_days": float64(7),
		"max_files":      uint16(12),
		"max_total_size": json.Number("4096"),
	})
	if err != nil {
		t.Fatalf("合法日志配置解析失败: %v", err)
	}
	if options.MaxFileSize != 1024 || options.RetentionDays != 7 || options.MaxFiles != 12 || options.MaxTotalSize != 4096 {
		t.Fatalf("日志配置解析结果错误: %#v", options)
	}

	invalidCases := []map[string]interface{}{
		{"max_file_size": 1.5},
		{"retention_days": "30"},
		{"max_files": true},
		{"max_total_size": uint64(1) << 63},
		{"max_total_size": json.Number("1.25")},
	}
	for _, invalid := range invalidCases {
		if _, err := readLogFileOptions(invalid); err == nil {
			t.Fatalf("非法日志整数配置应返回错误: %#v", invalid)
		}
	}
}

// TestCreateAppLogChannelRejectsUnsupportedAndNegativeConfiguration 验证通道构造不会对错误类型或负数配置静默回退。
func TestCreateAppLogChannelRejectsUnsupportedAndNegativeConfiguration(t *testing.T) {
	app := &App{BasePath: t.TempDir()}
	testCases := []map[string]interface{}{
		{"type": "unknown"},
		{"type": "file", "max_files": -1},
		{"type": "file", "level": []interface{}{"info", 7}},
		{"type": "file", "path": ""},
	}
	for _, config := range testCases {
		if logger, err := createAppLogChannel(app, config, false); err == nil {
			if logger != nil {
				_ = logger.Close()
			}
			t.Fatalf("非法日志通道配置应返回错误: %#v", config)
		}
	}
}

// TestCreateAppLogChannelResolvesRelativePathFromApplicationRoot 验证日志路径不依赖进程工作目录。
func TestCreateAppLogChannelResolvesRelativePathFromApplicationRoot(t *testing.T) {
	basePath := t.TempDir()
	app := &App{BasePath: basePath}
	logger, err := createAppLogChannel(app, map[string]interface{}{
		"type": "file",
		"path": filepath.Join("runtime", "relative-log"),
	}, false)
	if err != nil {
		t.Fatalf("创建相对路径日志通道失败: %v", err)
	}
	defer func() { _ = logger.Close() }()
	expected := filepath.Join(basePath, "runtime", "relative-log")
	if info, err := os.Stat(expected); err != nil || !info.IsDir() {
		t.Fatalf("相对日志目录应创建在应用根目录下，路径=%s 错误=%v", expected, err)
	}
}

// TestInitializeReportsMissingDefaultLogChannel 验证默认通道不存在时不会静默使用构造期临时日志器。
func TestInitializeReportsMissingDefaultLogChannel(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	if err := os.WriteFile(
		filepath.Join(basePath, "config", "log.json"),
		[]byte(`{"default":"missing","channels":{"file":{"type":"file"}}}`),
		0o600,
	); err != nil {
		t.Fatalf("覆盖日志配置失败: %v", err)
	}

	app := NewApp(basePath)
	defer func() {
		if app.Log != nil {
			_ = app.Log.Close()
		}
		if app.DB != nil {
			_ = app.DB.Close()
		}
	}()
	err := app.StartupError()
	if err == nil || !strings.Contains(err.Error(), "默认日志通道") {
		t.Fatalf("缺失默认日志通道应产生启动错误，实际为 %v", err)
	}
}
