package framework

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/cache"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver"
	"github.com/zhuhanxin0308/thinkgo/framework/cookie"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/filesystem"
	"github.com/zhuhanxin0308/thinkgo/framework/session"
	"github.com/zhuhanxin0308/thinkgo/framework/view"
)

// TestAppCoreFacadesHideContainerDetails 验证业务可以通过 App 直接取得所有
// 核心服务，且 facade 返回的就是应用生命周期管理的实例。
func TestAppCoreFacadesHideContainerDetails(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	app.cache = cache.NewCache(nil, cacheDriver.NewMemory())
	app.filesystem = filesystem.New(map[string]interface{}{})
	app.cookie = &cookie.Cookie{}
	app.session = &session.Session{}
	app.db = &db.DB{}
	app.dbManager = db.NewManager("default")
	app.view = &view.View{}

	if app.Container() != app.container || app.Config() != app.config || app.Env() != app.env || app.Event() != app.event {
		t.Fatal("App 基础服务 facade 未返回应用持有实例")
	}
	if app.Route() != app.routeFacade || app.Middleware() != app.middleware || app.Log() != app.log || app.Lang() != app.lang {
		t.Fatal("App HTTP 与基础设施 facade 未返回应用持有实例")
	}
	if app.Cache() != app.cache || app.Filesystem() != app.filesystem || app.Cookie() != app.cookie || app.Session() != app.session {
		t.Fatal("App 状态服务 facade 未返回应用持有实例")
	}
	if app.DB() != app.db || app.DBManager() != app.dbManager || app.View() != app.view {
		t.Fatal("App 数据库或视图 facade 未返回应用持有实例")
	}
	if app.DebugManager() != app.debug || app.Metrics() != app.metrics || app.Health() != app.health {
		t.Fatal("App 可观测性 facade 未返回应用持有实例")
	}
	if app.Migration() != app.migrations || app.Telemetry() != app.telemetry {
		t.Fatal("App 迁移或链路追踪 facade 未返回应用持有实例")
	}

	withoutDatabase := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = withoutDatabase.Close() })
	withoutDatabase.dbManager = db.NewManager("default")
	if withoutDatabase.DB() != nil {
		t.Fatal("默认连接不存在时 DB facade 必须返回 nil，而不是泄露管理器错误")
	}

	var nilApp *App
	if nilApp.Container() != nil || nilApp.Config() != nil || nilApp.Env() != nil || nilApp.Event() != nil || nilApp.Route() != nil || nilApp.Middleware() != nil {
		t.Fatal("nil App 基础 facade 必须安全返回 nil")
	}
	if nilApp.Log() != nil || nilApp.Lang() != nil || nilApp.Cache() != nil || nilApp.Filesystem() != nil || nilApp.Cookie() != nil || nilApp.Session() != nil {
		t.Fatal("nil App 状态服务 facade 必须安全返回 nil")
	}
	if nilApp.DB() != nil || nilApp.DBManager() != nil || nilApp.View() != nil || nilApp.DebugManager() != nil || nilApp.Metrics() != nil || nilApp.Health() != nil || nilApp.Migration() != nil || nilApp.Telemetry() != nil {
		t.Fatal("nil App 数据与可观测性 facade 必须安全返回 nil")
	}
}

// TestAppThinkPHPPathsAndOverrides 验证 app、runtime、public、view 等路径
// 按 ThinkPHP 单应用规则解析相对值、绝对值和默认值。
func TestAppThinkPHPPathsAndOverrides(t *testing.T) {
	basePath := t.TempDir()
	app := NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })

	if err := app.SetAppPath("domain"); err != nil {
		t.Fatalf("设置相对 app 路径失败: %v", err)
	}
	if app.GetAppPath() != filepath.Join(basePath, "domain") || app.ApplicationLangPath() != filepath.Join(basePath, "domain", "lang") {
		t.Fatalf("相对 app 路径解析错误: app=%q lang=%q", app.GetAppPath(), app.ApplicationLangPath())
	}
	if err := app.SetRuntimePath("var/runtime"); err != nil {
		t.Fatalf("设置相对 runtime 路径失败: %v", err)
	}
	expectedRuntime := filepath.Join(basePath, "var", "runtime")
	if app.GetRuntimePath() != expectedRuntime || app.RuntimeLogPath() != filepath.Join(expectedRuntime, "log") ||
		app.RuntimeCachePath() != filepath.Join(expectedRuntime, "cache") || app.RuntimeSessionPath() != filepath.Join(expectedRuntime, "session") {
		t.Fatalf("runtime 子目录错误: runtime=%q log=%q cache=%q session=%q", app.GetRuntimePath(), app.RuntimeLogPath(), app.RuntimeCachePath(), app.RuntimeSessionPath())
	}
	if app.ExceptionTemplatePath() != filepath.Join(basePath, "framework", "exception", "tpl") {
		t.Fatalf("异常模板目录错误: %q", app.ExceptionTemplatePath())
	}

	t.Setenv("APP_PUBLIC_PATH", "assets/public")
	if app.ProjectPublicPath() != filepath.Join(basePath, "assets", "public") {
		t.Fatalf("相对 APP_PUBLIC_PATH 错误: %q", app.ProjectPublicPath())
	}
	absolutePublic := filepath.Join(t.TempDir(), "public")
	t.Setenv("APP_PUBLIC_PATH", absolutePublic)
	if app.ProjectPublicPath() != absolutePublic {
		t.Fatalf("绝对 APP_PUBLIC_PATH 错误: %q", app.ProjectPublicPath())
	}
	t.Setenv("APP_PUBLIC_PATH", "")
	if err := app.config.Set("app.public_path", "web"); err != nil {
		t.Fatalf("设置 public_path 配置失败: %v", err)
	}
	if app.ProjectPublicPath() != filepath.Join(basePath, "web") {
		t.Fatalf("配置 public_path 错误: %q", app.ProjectPublicPath())
	}
	if err := app.config.Set("app.public_path", absolutePublic); err != nil {
		t.Fatalf("设置绝对 public_path 配置失败: %v", err)
	}
	if app.ProjectPublicPath() != absolutePublic {
		t.Fatalf("绝对配置 public_path 错误: %q", app.ProjectPublicPath())
	}

	for _, invalid := range []string{"", " leading", "trailing ", "bad\npath"} {
		if err := app.SetAppPath(invalid); err == nil {
			t.Errorf("非法 app 路径 %q 必须拒绝", invalid)
		}
		if err := app.SetRuntimePath(invalid); err == nil {
			t.Errorf("非法 runtime 路径 %q 必须拒绝", invalid)
		}
	}
	var nilApp *App
	if err := nilApp.SetAppPath("app"); err != ErrNilApplication {
		t.Fatalf("nil App.SetAppPath 错误: %v", err)
	}
	if err := nilApp.SetRuntimePath("runtime"); err != ErrNilApplication {
		t.Fatalf("nil App.SetRuntimePath 错误: %v", err)
	}
	if nilApp.GetRootPath() != "" || nilApp.GetAppPath() != "" || nilApp.ApplicationRuntimePath() != "" {
		t.Fatal("nil App 路径读取必须返回空字符串")
	}
}

// TestNormalizeViewConfigMatchesSingleApplicationLayout 验证兼容的 view、
// app/view 与自定义相对目录都解析到项目根单应用布局。
func TestNormalizeViewConfigMatchesSingleApplicationLayout(t *testing.T) {
	basePath := t.TempDir()
	app := NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	tests := []struct {
		name     string
		value    interface{}
		expected interface{}
	}{
		{name: "默认 view", value: "view", expected: filepath.Join(basePath, "view")},
		{name: "旧 app view", value: "app/view", expected: filepath.Join(basePath, "view")},
		{name: "旧 app", value: "app", expected: filepath.Join(basePath, "view")},
		{name: "自定义相对目录", value: "templates", expected: filepath.Join(basePath, "templates")},
		{name: "空目录", value: "", expected: ""},
		{name: "绝对目录", value: filepath.Join(basePath, "absolute"), expected: filepath.Join(basePath, "absolute")},
		{name: "非字符串", value: 7, expected: 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := map[string]interface{}{"view_path": test.value}
			app.normalizeViewConfig(configuration)
			if !reflect.DeepEqual(configuration["view_path"], test.expected) {
				t.Fatalf("view_path 规范化错误: actual=%#v expected=%#v", configuration["view_path"], test.expected)
			}
		})
	}
	var nilApp *App
	configuration := map[string]interface{}{"view_path": "view"}
	nilApp.normalizeViewConfig(configuration)
	if configuration["view_path"] != "view" {
		t.Fatal("nil App 不得修改 view 配置")
	}
}

// TestApplicationStateStringIsStable 验证所有生命周期阶段都有稳定、可诊断文本。
func TestApplicationStateStringIsStable(t *testing.T) {
	expected := map[ApplicationState]string{
		ApplicationStateInvalid:      "invalid",
		ApplicationStateConstructed:  "constructed",
		ApplicationStateInitializing: "initializing",
		ApplicationStateInitialized:  "initialized",
		ApplicationStateBooting:      "booting",
		ApplicationStateRunning:      "running",
		ApplicationStateClosing:      "closing",
		ApplicationStateClosed:       "closed",
		ApplicationStateFailed:       "failed",
		ApplicationState(255):        "unknown",
	}
	for state, text := range expected {
		if actual := state.String(); actual != text {
			t.Errorf("生命周期状态 %d 文本错误: actual=%q expected=%q", state, actual, text)
		}
	}
	if (*App)(nil).State() != ApplicationStateInvalid {
		t.Fatal("nil App 状态必须为 invalid")
	}
}
