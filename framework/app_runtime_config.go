package framework

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"thinkgo/framework/db"
)

const defaultApplicationDisplayName = "ThinkGo"

// DisplayName 返回 app.app_name 配置对应的应用显示名称。
func (app *App) DisplayName() string {
	if app == nil {
		return ""
	}
	if name := strings.TrimSpace(app.AppName); name != "" {
		return name
	}
	return strings.TrimSpace(app.ApplicationName)
}

// Location 返回当前应用独立使用的时区，避免多应用之间修改进程全局时区。
func (app *App) Location() *time.Location {
	if app == nil || app.Timezone == nil {
		return time.Local
	}
	return app.Timezone
}

// Now 返回当前应用时区下的当前时间。
func (app *App) Now() time.Time {
	return time.Now().In(app.Location())
}

// applyApplicationRuntimeConfig 将 app.json 的运行时字段转换为明确的应用状态。
func (app *App) applyApplicationRuntimeConfig() {
	if app == nil || app.config == nil {
		return
	}
	if strings.TrimSpace(app.AppName) == "" {
		app.AppName = defaultApplicationDisplayName
	}
	if app.Timezone == nil {
		app.Timezone = time.Local
	}

	if app.config.Has("app.app_name") {
		raw := app.config.Get("app.app_name")
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" || hasApplicationControl(value) {
			app.recordStartupError(fmt.Errorf("app.app_name 必须是非空且不含控制字符的字符串"))
		} else {
			app.AppName = value
		}
	}
	if app.config.Has("app.app_env") {
		raw := app.config.Get("app.app_env")
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" || hasApplicationControl(value) {
			app.recordStartupError(fmt.Errorf("app.app_env 必须是非空且不含控制字符的字符串"))
		}
	}
	if app.config.Has("app.default_timezone") {
		raw := app.config.Get("app.default_timezone")
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" || hasApplicationControl(value) {
			app.recordStartupError(fmt.Errorf("app.default_timezone 必须是非空且不含控制字符的字符串"))
		} else if location, err := loadApplicationLocation(value); err != nil {
			app.recordStartupError(fmt.Errorf("app.default_timezone %q 加载失败: %w", value, err))
		} else {
			app.Timezone = location
			app.timezoneConfigured = true
		}
	}

	app.DebugMode = app.readApplicationBool("app.app_debug", false)
	app.traceEnabled = app.readApplicationBool("app.app_trace", false)
	app.metricsEnabled = app.readApplicationBool("app.metrics_enable", false)
	app.operationalRoutesEnabled = app.readApplicationBool("app.operational_routes_enable", false)
	app.sessionEnabled = app.readApplicationBool("app.session_enable", false)
	app.csrfEnabled = app.readApplicationBool("app.csrf_enable", false)
	if app.debug != nil {
		app.debug.Enabled = app.traceEnabled
		app.debug.SetLocation(app.Location())
	}
	if app.log != nil {
		app.log.SetLocation(app.Location())
	}
	if app.metricsEnabled && app.metrics != nil {
		app.metrics.Enable()
	}
	if app.container != nil {
		app.Instance("app.name", app.DisplayName())
		app.Instance("app.timezone", app.Location())
	}
}

// readApplicationBool 严格读取应用布尔字段，防止非法配置被静默当成 false。
func (app *App) readApplicationBool(path string, fallback bool) bool {
	if app == nil || app.config == nil || !app.config.Has(path) {
		return fallback
	}
	value, err := app.config.GetBoolStrict(path)
	if err != nil || app.config.Get(path) == nil {
		if err == nil {
			err = fmt.Errorf("必须是布尔值")
		}
		app.recordStartupError(fmt.Errorf("%s 配置无效: %w", path, err))
		return fallback
	}
	return value
}

// applyDatabaseTimezone 为内置数据库连接注入应用时区，保证驱动解析和数据库会话时钟一致。
func (app *App) applyDatabaseTimezone(configuration *db.Config) {
	if app == nil || configuration == nil || !app.timezoneConfigured {
		return
	}
	locationName := app.Location().String()
	if locationName == "" || locationName == "Local" {
		return
	}
	if configuration.Params == nil {
		configuration.Params = make(map[string]string)
	}

	switch strings.ToLower(strings.TrimSpace(configuration.Type)) {
	case "mysql":
		// loc 控制驱动把无时区数据库值解析为应用时区。
		setDatabaseParameterIfMissing(configuration.Params, "loc", locationName)
		// time_zone 控制 MySQL Session 函数和 TIMESTAMP 转换使用的时区。
		setDatabaseParameterIfMissing(configuration.Params, "time_zone", mysqlSessionTimezoneValue(locationName))
	case "pgsql", "postgres", "postgresql":
		// PostgreSQL 的 TimeZone 是每条连接的启动参数。
		setDatabaseParameterIfMissing(configuration.Params, "TimeZone", locationName)
	case "sqlite":
		// go-sqlite3 使用 _loc 解析原生时间值；Unix/字符串字段不受影响。
		setDatabaseParameterIfMissing(configuration.Params, "_loc", locationName)
	case "sqlsrv", "sqlserver":
		// go-mssqldb 的 timezone 同时控制 datetime 编解码。
		setDatabaseParameterIfMissing(configuration.Params, "timezone", locationName)
	case "oracle":
		// godror 的 timezone 参数控制每个连接的时间值解码。
		setDatabaseParameterIfMissing(configuration.Params, "timezone", locationName)
	}
}

func setDatabaseParameterIfMissing(parameters map[string]string, key string, value string) {
	for existing := range parameters {
		if strings.EqualFold(strings.TrimSpace(existing), key) {
			return
		}
	}
	parameters[key] = value
}

func mysqlSessionTimezoneValue(locationName string) string {
	return "'" + strings.ReplaceAll(locationName, "'", "''") + "'"
}

// validateConsoleApplicationConfig 让控制台配置在应用启动时就进入统一错误链。
func (app *App) validateConsoleApplicationConfig() {
	if app == nil || app.config == nil {
		return
	}
	values := app.config.GetMap("console")
	for key := range values {
		switch key {
		case "name", "version", "user", "auto_path":
		default:
			app.recordStartupError(fmt.Errorf("console 配置包含未知字段 %q", key))
		}
	}
	if raw, exists := values["name"]; exists {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" || hasApplicationControl(value) {
			app.recordStartupError(fmt.Errorf("console.name 必须是非空且不含控制字符的字符串"))
		}
	}
	if raw, exists := values["version"]; exists {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" || hasApplicationControl(value) {
			app.recordStartupError(fmt.Errorf("console.version 必须是非空且不含控制字符的字符串"))
		}
	}
	if raw, exists := values["user"]; exists && raw != nil {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) != value || hasApplicationControl(value) {
			app.recordStartupError(fmt.Errorf("console.user 必须是安全字符串或 null"))
		}
	}
	if raw, exists := values["auto_path"]; exists {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) != value || hasApplicationControl(value) {
			app.recordStartupError(fmt.Errorf("console.auto_path 必须是安全路径字符串"))
			return
		}
		if value != "" && !applicationPathWithinBase(app.BasePath, value) {
			app.recordStartupError(fmt.Errorf("console.auto_path 必须位于项目根目录下: %s", value))
		}
	}
}

func loadApplicationLocation(value string) (*time.Location, error) {
	value = strings.TrimSpace(value)
	if value == "Local" {
		return time.Local, nil
	}
	return time.LoadLocation(value)
}

func applicationPathWithinBase(basePath, configuredPath string) bool {
	baseAbsolute, err := filepath.Abs(strings.TrimSpace(basePath))
	if err != nil {
		return false
	}
	candidate := configuredPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(baseAbsolute, candidate)
	}
	candidate, err = filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(baseAbsolute, candidate)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func hasApplicationControl(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
