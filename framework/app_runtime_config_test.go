package framework

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thinkgo/framework/config"
	"thinkgo/framework/context"
	"thinkgo/framework/db"
	"thinkgo/framework/debug"
	"thinkgo/framework/env"
	"thinkgo/framework/metrics"
	"thinkgo/framework/middleware"
)

// TestApplicationRuntimeConfigAppliesAllApplicationFields 验证 app.json 的应用名称、环境、时区和开关字段均落入运行时状态。
func TestApplicationRuntimeConfigAppliesAllApplicationFields(t *testing.T) {
	app := &App{
		BasePath:  t.TempDir(),
		AppName:   defaultApplicationDisplayName,
		config:    config.NewConfig(),
		container: NewContainer(),
		env:       env.NewEnv(),
		debug:     debug.NewDebug(),
		metrics:   metrics.NewRegistry(),
	}
	app.config.Set("app.app_name", "订单中心")
	app.config.Set("app.app_env", "staging")
	app.config.Set("app.default_timezone", "Asia/Shanghai")
	app.config.Set("app.app_debug", true)
	app.config.Set("app.app_trace", "1")
	app.config.Set("app.metrics_enable", true)
	app.config.Set("app.session_enable", true)
	app.config.Set("app.csrf_enable", true)

	app.applyApplicationRuntimeConfig()

	if app.DisplayName() != "订单中心" || app.Environment() != "staging" {
		t.Fatalf("应用名称或环境未应用: name=%q environment=%q", app.DisplayName(), app.Environment())
	}
	if app.Location().String() != "Asia/Shanghai" || app.Now().Location().String() != "Asia/Shanghai" {
		t.Fatalf("应用时区未应用: location=%q now=%q", app.Location(), app.Now().Location())
	}
	if !app.DebugMode || !app.debug.Enabled || !app.metricsEnabled || !app.sessionEnabled || !app.csrfEnabled {
		t.Fatalf("应用开关未应用: debug=%t trace=%t metrics=%t session=%t csrf=%t", app.DebugMode, app.debug.Enabled, app.metricsEnabled, app.sessionEnabled, app.csrfEnabled)
	}
	if !app.metrics.Enabled() {
		t.Fatal("metrics_enable=true 应打开指标注册表")
	}
	if app.Get("app.name") != "订单中心" {
		t.Fatalf("应用名称未进入容器实例: %#v", app.Get("app.name"))
	}
}

// TestApplicationTimezoneEnvironmentOverride 验证部署环境可以覆盖应用默认时区。
func TestApplicationTimezoneEnvironmentOverride(t *testing.T) {
	basePath := t.TempDir()
	envFile := filepath.Join(basePath, ".env")
	if err := os.WriteFile(envFile, []byte("APP_DEFAULT_TIMEZONE=America/New_York\n"), 0o600); err != nil {
		t.Fatalf("写入测试环境配置失败: %v", err)
	}

	app := &App{
		BasePath: basePath,
		config:   config.NewConfig(),
		env:      env.NewEnv(),
		Timezone: time.Local,
	}
	if err := app.env.Load(envFile); err != nil {
		t.Fatalf("加载测试环境配置失败: %v", err)
	}
	app.applyStringEnvironmentOverride("APP_DEFAULT_TIMEZONE", "app.default_timezone")
	app.applyApplicationRuntimeConfig()

	if got := app.Location().String(); got != "America/New_York" {
		t.Fatalf("环境变量未覆盖应用时区: got=%q", got)
	}
}

// TestApplicationRuntimeConfigRejectsInvalidValues 验证应用字段非法时记录启动错误并使用安全默认值。
func TestApplicationRuntimeConfigRejectsInvalidValues(t *testing.T) {
	app := &App{config: config.NewConfig(), Timezone: nil}
	app.config.Set("app.app_name", "\r\n")
	app.config.Set("app.app_env", 123)
	app.config.Set("app.default_timezone", "Not/AZone")
	app.config.Set("app.app_debug", "maybe")
	app.applyApplicationRuntimeConfig()

	startupErr := app.StartupError()
	if startupErr == nil || !strings.Contains(startupErr.Error(), "default_timezone") || !strings.Contains(startupErr.Error(), "app_debug") {
		t.Fatalf("非法应用字段应进入启动错误: %v", startupErr)
	}
	if app.DebugMode || app.Location() == nil || app.DisplayName() == "\r\n" {
		t.Fatalf("非法应用字段不应污染运行时默认值: debug=%t location=%v name=%q", app.DebugMode, app.Location(), app.DisplayName())
	}
}

// TestApplicationTimezoneIsAppliedOnlyWhenDatabaseLocationIsImplicit 验证应用时区只补齐驱动缺失的时区参数。
func TestApplicationTimezoneIsAppliedOnlyWhenDatabaseLocationIsImplicit(t *testing.T) {
	app := &App{Timezone: mustTestLocation(t, "Asia/Shanghai"), timezoneConfigured: true}

	mysql := db.Config{Type: "mysql"}
	app.applyDatabaseTimezone(&mysql)
	if mysql.Params["loc"] != "Asia/Shanghai" {
		t.Fatalf("MySQL 未显式 loc 时应继承应用时区: %#v", mysql.Params)
	}
	if mysql.Params["time_zone"] != "'Asia/Shanghai'" {
		t.Fatalf("MySQL Session 时区未继承应用时区: %#v", mysql.Params)
	}

	explicit := db.Config{Type: "mysql", Params: map[string]string{"Loc": "UTC"}}
	app.applyDatabaseTimezone(&explicit)
	if explicit.Params["Loc"] != "UTC" || explicit.Params["time_zone"] != "'Asia/Shanghai'" || len(explicit.Params) != 2 {
		t.Fatalf("显式 loc 不应被覆盖: %#v", explicit.Params)
	}

	expectedParameters := map[string]struct {
		key   string
		value string
	}{
		"pgsql":  {key: "TimeZone", value: "Asia/Shanghai"},
		"sqlite": {key: "_loc", value: "Asia/Shanghai"},
		"sqlsrv": {key: "timezone", value: "Asia/Shanghai"},
		"oracle": {key: "timezone", value: "Asia/Shanghai"},
	}
	for databaseType, expected := range expectedParameters {
		other := db.Config{Type: databaseType}
		app.applyDatabaseTimezone(&other)
		if len(other.Params) != 1 || other.Params[expected.key] != expected.value {
			t.Fatalf("%s 应注入一个驱动时区参数: %#v", databaseType, other.Params)
		}
	}
}

// TestConsoleConfigIsValidatedDuringApplicationInitialization 验证控制台危险路径不会等到命令执行时才被发现。
func TestConsoleConfigIsValidatedDuringApplicationInitialization(t *testing.T) {
	app := &App{BasePath: t.TempDir(), config: config.NewConfig()}
	app.config.Set("console.auto_path", "../outside")
	app.validateConsoleApplicationConfig()
	if startupErr := app.StartupError(); startupErr == nil || !strings.Contains(startupErr.Error(), "console.auto_path") {
		t.Fatalf("非法控制台输出目录应进入启动错误: %v", startupErr)
	}
}

// TestMiddlewareConfigAppliesAliasesAndPriorityTypes 验证 JSON 数组和代码配置数组都能驱动中间件别名与优先级。
func TestMiddlewareConfigAppliesAliasesAndPriorityTypes(t *testing.T) {
	app := &App{config: config.NewConfig(), middleware: middleware.NewPipeline()}
	base := middleware.Handler(func(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
		return next(req)
	})
	app.middleware.Alias("base", base)
	app.config.Set("middleware.alias", map[string]interface{}{"public": "base"})
	app.config.Set("middleware.priority", []string{"public"})

	app.applyMiddlewareConfig()
	if app.middleware.ResolveAlias("public") == nil {
		t.Fatal("middleware.alias 未解析已注册别名")
	}
	if startupErr := app.StartupError(); startupErr != nil {
		t.Fatalf("合法中间件配置不应产生启动错误: %v", startupErr)
	}
}

func mustTestLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}
	return location
}
