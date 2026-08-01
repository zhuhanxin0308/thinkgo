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
	app := &App{DebugMode: true, env: frameworkenv.NewEnv()}
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
type initTestConnection struct {
	identity db.ConnectionID
}

func (c *initTestConnection) ConnectionID() db.ConnectionID {
	return c.identity
}

func (c *initTestConnection) Select(context.Context, db.SelectRequest) ([]map[string]interface{}, error) {
	return nil, nil
}

func (c *initTestConnection) Insert(context.Context, db.InsertRequest) (db.InsertResult, error) {
	return db.InsertResult{Affected: 1}, nil
}

func (c *initTestConnection) Update(context.Context, db.UpdateRequest) (db.UpdateResult, error) {
	return db.UpdateResult{Affected: 1}, nil
}

func (c *initTestConnection) Delete(context.Context, db.DeleteRequest) (db.DeleteResult, error) {
	return db.DeleteResult{Deleted: 1}, nil
}

func (c *initTestConnection) Count(context.Context, db.CountRequest) (int64, error) {
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
	return &initTestConnection{identity: db.NewConnectionID("app-init-test")}, nil
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
  "app_env": "test",
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
				"type": "memory", "max_entries": 2,
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
	for _, entry := range []struct{ key, value string }{
		{key: "first", value: "1"},
		{key: "second", value: "2"},
		{key: "third", value: "3"},
	} {
		if err = memoryStore.Set(entry.key, entry.value, 0); err != nil {
			t.Fatalf("写入有界 memory store 失败: key=%s err=%v", entry.key, err)
		}
	}
	if _, found, getErr := memoryStore.Get("first"); getErr != nil || found {
		t.Fatalf("memory store 超过容量后应淘汰最早条目: found=%t err=%v", found, getErr)
	}
	if _, found, getErr := memoryStore.Get("third"); getErr != nil || !found {
		t.Fatalf("memory store 最新条目不应被淘汰: found=%t err=%v", found, getErr)
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
		"memory 容量为负数": {
			"default": "memory",
			"stores": map[string]interface{}{
				"memory": map[string]interface{}{"type": "memory", "max_entries": -1},
			},
		},
		"memory 容量不是整数": {
			"default": "memory",
			"stores": map[string]interface{}{
				"memory": map[string]interface{}{"type": "memory", "max_entries": 1.5},
			},
		},
		"Redis 类型错误": {
			"default": "redis",
			"stores": map[string]interface{}{
				"redis": map[string]interface{}{"type": "redis", "timeout_ms": "slow"},
			},
		},
		"Redis 缺少安全命名空间": {
			"default": "redis",
			"stores": map[string]interface{}{
				"redis": map[string]interface{}{"type": "redis"},
			},
		},
		"重复文件缓存目录": {
			"default": "file",
			"stores": map[string]interface{}{
				"file":   map[string]interface{}{"type": "file", "path": "./runtime/cache"},
				"backup": map[string]interface{}{"type": "file", "path": "runtime/cache"},
			},
		},
		"重复 Redis 命名空间": {
			"default": "redis",
			"stores": map[string]interface{}{
				"redis":  map[string]interface{}{"type": "redis", "prefix": "thinkgo:"},
				"backup": map[string]interface{}{"type": "redis", "prefix": "thinkgo:"},
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

// TestCreateAppCacheRejectsRelativeSymlinkEscape 验证相对缓存路径不能通过 runtime 内符号链接逃出应用目录。
func TestCreateAppCacheRejectsRelativeSymlinkEscape(t *testing.T) {
	basePath := t.TempDir()
	runtimePath := filepath.Join(basePath, "runtime")
	outsidePath := filepath.Join(basePath, "outside")
	if err := os.MkdirAll(runtimePath, 0o700); err != nil {
		t.Fatalf("创建 runtime 测试目录失败: %v", err)
	}
	if err := os.MkdirAll(outsidePath, 0o700); err != nil {
		t.Fatalf("创建外部测试目录失败: %v", err)
	}
	linkPath := filepath.Join(runtimePath, "linked")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("当前环境不允许创建符号链接: %v", err)
	}
	config := map[string]interface{}{
		"default": "file",
		"stores": map[string]interface{}{
			"file": map[string]interface{}{"type": "file", "path": "runtime/linked/cache"},
		},
	}
	manager, err := createAppCache(&App{BasePath: basePath}, config)
	if manager != nil || err == nil {
		t.Fatalf("符号链接逃逸路径应失败且不返回管理器: manager=%#v err=%v", manager, err)
	}
	if _, statErr := os.Stat(filepath.Join(outsidePath, "cache")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("拒绝路径后不应在 runtime 外创建缓存目录: %v", statErr)
	}
}

// TestInitializeDoesNotFallbackFromInvalidCacheConfig 验证应用启动不会把非法缓存配置静默替换成文件驱动。
// TestCreateAppCacheRejectsRedisPrefixWithWrongTypeWhenFlushAuthorized 验证危险授权不会放宽命名空间类型校验。
func TestCreateAppCacheRejectsRedisPrefixWithWrongTypeWhenFlushAuthorized(t *testing.T) {
	basePath := t.TempDir()
	config := map[string]interface{}{
		"default": "redis",
		"stores": map[string]interface{}{
			"redis": map[string]interface{}{
				"type": "redis", "prefix": true, "allow_flush_db": true,
			},
		},
	}
	if manager, err := createAppCache(&App{BasePath: basePath}, config); manager != nil || err == nil {
		t.Fatalf("prefix 类型错误不应被 allow_flush_db 放宽: manager=%#v err=%v", manager, err)
	}
}

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
	app, _ := initializeTestConsoleApp(t, basePath)
	if startupErr := app.StartupError(); startupErr == nil || !strings.Contains(startupErr.Error(), "初始化缓存失败") {
		t.Fatalf("非法缓存配置未进入启动错误: %v", startupErr)
	}
	if _, _, err := app.cache.Get("key"); !errors.Is(err, cache.ErrCacheDriverNotConfigured) {
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
	app := mustBuildTestConsoleApp(t, basePath)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.StartupError(); err != nil {
		t.Fatalf("合法安全模块配置不应产生启动错误: %v", err)
	}
	if app.cookie == nil || app.session == nil || app.middleware.ResolveAlias("csrf") == nil {
		t.Fatal("Cookie、Session 与 CSRF 服务必须全部初始化")
	}
	if app.Get("cookie") != app.cookie || app.Get("session") != app.session {
		t.Fatal("Cookie 与 Session 必须绑定到应用容器")
	}
	expectedStorage := filepath.Clean(filepath.Join(basePath, "runtime", "session"))
	if app.session.GetConfig().StoragePath != expectedStorage {
		t.Fatalf("Session 相对路径未基于应用根目录解析: %q", app.session.GetConfig().StoragePath)
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
			app, _ := initializeTestConsoleApp(t, basePath)
			if err := app.StartupError(); err == nil || !strings.Contains(err.Error(), test.errorMatch) {
				t.Fatalf("非法配置应进入启动错误并包含 %q，实际为 %v", test.errorMatch, err)
			}
			if app.cookie == nil || app.session == nil || app.middleware.ResolveAlias("csrf") == nil {
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

	app, _ := initializeTestConsoleApp(t, basePath)
	if err := app.StartupError(); err == nil || !strings.Contains(err.Error(), "load environment failed") {
		t.Fatalf("损坏 .env 应进入启动错误，实际为 %v", err)
	}
}

// TestInitializeRejectsInvalidBooleanEnvironmentOverride 验证非法布尔环境变量不会被静默解释为 false。
func TestInitializeRejectsInvalidBooleanEnvironmentOverride(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	t.Setenv("APP_DEBUG", "not-a-boolean")

	app, _ := initializeTestConsoleApp(t, basePath)
	if err := app.StartupError(); err == nil || !strings.Contains(err.Error(), "APP_DEBUG") {
		t.Fatalf("非法 APP_DEBUG 应进入启动错误，实际为 %v", err)
	}
}

// TestInitializeAppliesSecurityEnvironmentOverrides 验证生产部署可通过环境变量注入安全配置。
func TestInitializeAppliesSecurityEnvironmentOverrides(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	cookieSecret := strings.Repeat("c", 32)
	csrfSecret := strings.Repeat("s", 32)
	t.Setenv("COOKIE_SECRET", cookieSecret)
	t.Setenv("CSRF_SECRET", csrfSecret)
	t.Setenv("APP_CSRF_ENABLE", "true")

	app := mustBuildTestConsoleApp(t, basePath)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.StartupError(); err != nil {
		t.Fatalf("合法安全环境变量不应产生启动错误: %v", err)
	}
	if app.cookie == nil {
		t.Fatal("Cookie 服务必须完成初始化")
	}
	if got := app.cookie.GetConfig().Secret; got != cookieSecret {
		t.Fatalf("COOKIE_SECRET 未覆盖 Cookie 配置，实际为 %q", got)
	}
	if got := app.config.GetString("csrf.secret"); got != csrfSecret {
		t.Fatalf("CSRF_SECRET 未覆盖 CSRF 配置，实际为 %q", got)
	}
	if !app.config.GetBool("app.csrf_enable", false) {
		t.Fatal("APP_CSRF_ENABLE=true 应启用全局 CSRF")
	}
}

// TestInitializeRejectsInvalidCSRFEnvironmentOverride 验证非法 CSRF 开关不会被静默解释为 false。
func TestInitializeRejectsInvalidCSRFEnvironmentOverride(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	t.Setenv("APP_CSRF_ENABLE", "not-a-boolean")

	app, _ := initializeTestConsoleApp(t, basePath)
	if err := app.StartupError(); err == nil || !strings.Contains(err.Error(), "APP_CSRF_ENABLE") {
		t.Fatalf("非法 APP_CSRF_ENABLE 应进入启动错误，实际为 %v", err)
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
	app := mustBuildTestConsoleApp(t, basePath)
	t.Cleanup(func() { _ = app.Close() })
	if got := app.Environment(); got != "staging" {
		t.Fatalf("Environment 应读取 .env 兜底，实际为 %q", got)
	}
}

// TestInitializeIsIdempotent 验证重复初始化不会替换资源或叠加后台任务和中间件。
func TestInitializeIsIdempotent(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app := mustBuildTestConsoleApp(t, basePath)
	t.Cleanup(func() { _ = app.Close() })

	manager := app.dbManager
	logger := app.log
	pipeline := app.middleware
	if err := app.Initialize(); err != nil {
		t.Fatalf("重复初始化应返回首次结果，实际为 %v", err)
	}
	if app.dbManager != manager || app.log != logger || app.middleware != pipeline {
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
	langDir := filepath.Join(basePath, "app", "index", "lang")
	if err := os.MkdirAll(langDir, 0o755); err != nil {
		t.Fatalf("创建语言目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(langDir, "zh-cn.json"), []byte(`{"auth":`), 0o644); err != nil {
		t.Fatalf("写入损坏语言包失败: %v", err)
	}

	app, _ := initializeTestApp(t, basePath)

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

	app, _ := initializeTestConsoleApp(t, basePath)
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

	app := mustBuildTestApp(t, basePath)
	defer func() {
		if app.log != nil {
			_ = app.log.Close()
		}
		if app.db != nil {
			_ = app.db.Close()
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
	appConfigPath := filepath.Join(basePath, "config", "app.json")
	appConfig, err := os.ReadFile(appConfigPath)
	if err != nil {
		t.Fatalf("读取测试应用配置失败: %v", err)
	}
	appConfig = []byte(strings.Replace(string(appConfig), `  "default_lang": "zh-cn",`, `  "default_timezone": "Asia/Shanghai",
  "default_lang": "zh-cn",`, 1))
	if err := os.WriteFile(appConfigPath, appConfig, 0o644); err != nil {
		t.Fatalf("写入测试应用时区失败: %v", err)
	}

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

	app := mustBuildTestApp(t, basePath)
	defer func() {
		if app.log != nil {
			_ = app.log.Close()
		}
		if app.db != nil {
			_ = app.db.Close()
		}
	}()

	if app.db == nil {
		t.Fatal("应用初始化后默认数据库连接不应为空")
	}
	if got := app.db.Location().String(); got != "Asia/Shanghai" {
		t.Fatalf("数据库未继承应用时区: got=%q", got)
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
