package framework

import (
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/debug"
	"github.com/zhuhanxin0308/thinkgo/v3/env"
	"github.com/zhuhanxin0308/thinkgo/v3/metrics"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// TestApplicationRuntimeConfigAppliesAllApplicationFields 验证 app.json 的应用名称、环境和开关字段均落入运行时状态。
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
	app.config.Set("app.app_debug", true)
	app.config.Set("app.metrics_enable", true)
	app.config.Set("app.session_enable", true)
	app.config.Set("app.csrf_enable", true)

	app.applyApplicationRuntimeConfig()

	if app.DisplayName() != "订单中心" || app.Environment() != "staging" {
		t.Fatalf("应用名称或环境未应用: name=%q environment=%q", app.DisplayName(), app.Environment())
	}
	if app.Location().String() != defaultTimezoneName || app.Now().Location().String() != defaultTimezoneName {
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

// TestApplicationRuntimeDebugFollowsFinalConfigurationSnapshot 验证 Debug 状态
// 每次都由当前最终配置快照决定，历史 true 和仍然存在的环境变量都不能绕过快照。
func TestApplicationRuntimeDebugFollowsFinalConfigurationSnapshot(t *testing.T) {
	application := &App{
		config: config.NewConfig(),
		env:    env.NewEnv(),
		debug:  debug.NewDebug(),
	}
	if err := application.config.Set("app.app_debug", true); err != nil {
		t.Fatalf("写入初始 Debug 配置失败: %v", err)
	}
	application.applyApplicationRuntimeConfig()
	if !application.IsDebug() || !application.debug.Enabled {
		t.Fatal("最终配置中的 true 应开启应用与 Debug 服务")
	}

	t.Setenv("APP_DEBUG", "true")
	if err := application.config.Set("app.app_debug", false); err != nil {
		t.Fatalf("写入重载后的 Debug 配置失败: %v", err)
	}
	application.applyApplicationRuntimeConfig()
	if application.IsDebug() || application.debug.Enabled {
		t.Fatal("最终配置中的 false 必须关闭历史 Debug 状态，不能被旧环境来源重新开启")
	}
}

// TestApplicationRuntimeDebugHonorsExplicitAPICall 验证公开 Debug API 是
// 明确覆盖而不是一次易被后续配置重算抹掉的临时字段写入。
func TestApplicationRuntimeDebugHonorsExplicitAPICall(t *testing.T) {
	application := &App{
		config: config.NewConfig(),
		debug:  debug.NewDebug(),
	}
	if err := application.config.Set("app.app_debug", false); err != nil {
		t.Fatalf("写入关闭 Debug 的配置失败: %v", err)
	}
	application.Debug(true)
	application.applyApplicationRuntimeConfig()
	if !application.IsDebug() || !application.debug.Enabled {
		t.Fatal("Debug(true) 必须覆盖配置中的 false")
	}

	if err := application.config.Set("app.app_debug", true); err != nil {
		t.Fatalf("写入开启 Debug 的配置失败: %v", err)
	}
	application.Debug(false)
	application.applyApplicationRuntimeConfig()
	if application.IsDebug() || application.debug.Enabled {
		t.Fatal("Debug(false) 必须覆盖配置中的 true")
	}
}

// TestApplicationRuntimeAppliesThinkPHPTimezoneConfiguration 验证 app.default_timezone
// 与 ThinkPHP 一样改变当前应用的日期时间语义。
func TestApplicationRuntimeAppliesThinkPHPTimezoneConfiguration(t *testing.T) {
	app := &App{config: config.NewConfig()}
	app.config.Set("app.default_timezone", "America/New_York")
	app.applyApplicationRuntimeConfig()

	if app.Location().String() != "America/New_York" {
		t.Fatalf("default_timezone 未应用: got=%q", app.Location())
	}
	if startupErr := app.StartupError(); startupErr != nil {
		t.Fatalf("合法时区不应产生启动错误: %v", startupErr)
	}
}

// TestApplicationRuntimeConfigRejectsInvalidValues 验证应用字段非法时记录启动错误并使用安全默认值。
func TestApplicationRuntimeConfigRejectsInvalidValues(t *testing.T) {
	app := &App{config: config.NewConfig()}
	app.config.Set("app.app_name", "\r\n")
	app.config.Set("app.app_env", 123)
	app.config.Set("app.app_debug", "maybe")
	app.applyApplicationRuntimeConfig()

	startupErr := app.StartupError()
	if startupErr == nil || !strings.Contains(startupErr.Error(), "app.app_env") || !strings.Contains(startupErr.Error(), "app_debug") {
		t.Fatalf("非法应用字段应进入启动错误: %v", startupErr)
	}
	if app.DebugMode || app.Location() == nil || app.DisplayName() == "\r\n" {
		t.Fatalf("非法应用字段不应污染运行时默认值: debug=%t location=%v name=%q", app.DebugMode, app.Location(), app.DisplayName())
	}
	if app.Location().String() != defaultTimezoneName {
		t.Fatalf("非法配置后的默认时区必须为 %s: %v", defaultTimezoneName, app.Location())
	}
}

// TestApplicationRuntimeConfigAppliesOperationalAndDependencyPolicies 验证安全、数据库启动和运维访问策略进入不可变运行时状态。
func TestApplicationRuntimeConfigAppliesOperationalAndDependencyPolicies(t *testing.T) {
	app := &App{config: config.NewConfig()}
	app.config.Set("app.security_profile", string(SecurityProfileStatelessAPI))
	app.config.Set("app.database_startup_policy", string(DatabaseStartupRequired))
	app.config.Set("app.operational_routes_access", string(OperationalAccessRestricted))
	app.config.Set("app.operational_routes_allowed_cidrs", []interface{}{"127.0.0.0/8", "::1/128"})

	app.applyApplicationRuntimeConfig()

	if app.securityProfile != SecurityProfileStatelessAPI || app.databaseStartupPolicy != DatabaseStartupRequired {
		t.Fatalf("应用策略未应用: security=%q database=%q", app.securityProfile, app.databaseStartupPolicy)
	}
	if app.operationalAccess != OperationalAccessRestricted || len(app.operationalAllowedPrefixes) != 2 {
		t.Fatalf("运维访问策略未应用: access=%q prefixes=%v", app.operationalAccess, app.operationalAllowedPrefixes)
	}
	if startupErr := app.StartupError(); startupErr != nil {
		t.Fatalf("合法应用策略不应产生启动错误: %v", startupErr)
	}
}

// TestApplicationRuntimeConfigRejectsInvalidPolicies 验证未知策略和全网段运维白名单不能静默进入运行态。
func TestApplicationRuntimeConfigRejectsInvalidPolicies(t *testing.T) {
	app := &App{config: config.NewConfig()}
	app.config.Set("app.security_profile", "unknown")
	app.config.Set("app.database_startup_policy", "sometimes")
	app.config.Set("app.operational_routes_access", string(OperationalAccessRestricted))
	app.config.Set("app.operational_routes_allowed_cidrs", []interface{}{"0.0.0.0/0"})

	app.applyApplicationRuntimeConfig()

	startupErr := app.StartupError()
	if startupErr == nil || !strings.Contains(startupErr.Error(), "security_profile") ||
		!strings.Contains(startupErr.Error(), "database_startup_policy") ||
		!strings.Contains(startupErr.Error(), "operational_routes_allowed_cidrs") {
		t.Fatalf("非法应用策略应完整进入启动错误: %v", startupErr)
	}
}

// TestApplicationDatabaseTimezoneParametersRemainDeveloperControlled 验证框架不再
// 覆盖 ThinkPHP 允许开发者通过数据库配置指定的时区参数。
func TestApplicationDatabaseTimezoneParametersRemainDeveloperControlled(t *testing.T) {
	configuration, err := readDatabaseConfig(map[string]interface{}{
		"type": "mysql",
		"params": map[string]interface{}{
			"loc":       "Asia/Shanghai",
			"time_zone": "'+08:00'",
		},
	})
	if err != nil {
		t.Fatalf("读取数据库配置失败: %v", err)
	}
	applyDatabaseFallbacks(&configuration)
	if configuration.Params["loc"] != "Asia/Shanghai" || configuration.Params["time_zone"] != "'+08:00'" {
		t.Fatalf("数据库时区参数不应被框架改写: %#v", configuration.Params)
	}
}

// TestApplicationTimezoneDefaultsToThinkPHP 验证空应用使用 ThinkPHP 默认时区。
func TestApplicationTimezoneDefaultsToThinkPHP(t *testing.T) {
	var nilApp *App
	if nilApp.Location().String() != defaultTimezoneName {
		t.Fatalf("空应用时区必须回退 %s: %v", defaultTimezoneName, nilApp.Location())
	}
	app := &App{}
	if app.Location().String() != defaultTimezoneName || app.Now().Location().String() != defaultTimezoneName {
		t.Fatalf("未配置应用时区必须为 %s: location=%v now=%v", defaultTimezoneName, app.Location(), app.Now().Location())
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
