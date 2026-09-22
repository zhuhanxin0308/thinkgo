package framework

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	_ "time/tzdata"
)

const (
	defaultApplicationDisplayName = "ThinkGo"
	defaultTimezoneName           = "Asia/Shanghai"
)

// DisplayName 返回 app.app_name 配置对应的应用显示名称。
func (app *App) DisplayName() string {
	if app == nil {
		return ""
	}
	if name := strings.TrimSpace(app.AppName); name != "" {
		return name
	}
	return defaultApplicationDisplayName
}

// Location 返回当前应用配置的时区。
// 默认值与 ThinkPHP 8.1.4 的 app.default_timezone 一致，为 Asia/Shanghai。
func (app *App) Location() *time.Location {
	if app == nil {
		return defaultApplicationLocation()
	}
	app.locationMu.RLock()
	location := app.location
	app.locationMu.RUnlock()
	if location == nil {
		return defaultApplicationLocation()
	}
	return location
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
	if app.config.Has("app.app_namespace") {
		raw := app.config.Get("app.app_namespace")
		namespace, ok := raw.(string)
		if !ok || hasApplicationControl(namespace) {
			app.recordStartupError(fmt.Errorf("app.app_namespace 必须是不含控制字符的字符串"))
		} else if strings.TrimSpace(namespace) != "" {
			app.SetNamespace(strings.TrimSpace(namespace))
		}
	}
	app.applyTimezoneConfig()
	// 普通状态由最终配置快照决定；只有公开 Debug API 的显式调用可以覆盖配置。
	debugEnabled := app.readApplicationBool("app.app_debug", false)
	app.metadataMu.RLock()
	if app.debugOverrideSet {
		debugEnabled = app.debugOverride
	}
	app.metadataMu.RUnlock()
	app.metadataMu.Lock()
	app.DebugMode = debugEnabled
	app.metadataMu.Unlock()
	// think-trace 服务始终注册中间件，并只由 App::isDebug 决定是否输出。
	app.traceEnabled = debugEnabled
	app.metricsEnabled = app.readApplicationBool("app.metrics_enable", false)
	app.operationalRoutesEnabled = app.readApplicationBool("app.operational_routes_enable", false)
	app.sessionEnabled = app.readApplicationBool("app.session_enable", false)
	app.csrfEnabled = app.readApplicationBool("app.csrf_enable", false)
	app.applyApplicationPolicies()
	if app.debug != nil {
		app.debug.Enabled = debugEnabled
		app.debug.SetLocation(app.Location())
	}
	if app.log != nil {
		app.log.SetLocation(app.Location())
	}
	if app.metrics != nil {
		if app.metricsEnabled {
			app.metrics.Enable()
		} else {
			app.metrics.Disable()
		}
	}
	if app.container != nil {
		if err := app.Instance("app.name", app.DisplayName()); err != nil {
			app.recordStartupError(fmt.Errorf("绑定应用显示名称失败: %w", err))
		}
		if err := app.Instance("app.timezone", app.Location()); err != nil {
			app.recordStartupError(fmt.Errorf("绑定应用时区失败: %w", err))
		}
	}
}

// WithRoute 返回是否加载 route 目录中的显式路由定义。
func (app *App) WithRoute() bool {
	if app == nil || app.config == nil {
		return true
	}
	return app.readApplicationBool("app.with_route", true)
}

func defaultApplicationLocation() *time.Location {
	location, err := time.LoadLocation(defaultTimezoneName)
	if err == nil {
		return location
	}
	// 内置 tzdata 正常情况下不会失败；固定偏移只作为损坏运行环境的确定性兜底。
	return time.FixedZone(defaultTimezoneName, 8*60*60)
}

// applyTimezoneConfig 读取并校验 app.default_timezone，配置错误进入统一启动错误链。
func (app *App) applyTimezoneConfig() {
	timezoneName := defaultTimezoneName
	if app.config.Has("app.default_timezone") {
		raw := app.config.Get("app.default_timezone")
		configured, ok := raw.(string)
		if !ok || strings.TrimSpace(configured) != configured || configured == "" || hasApplicationControl(configured) {
			app.recordStartupError(fmt.Errorf("app.default_timezone 必须是有效的非空时区名称"))
			return
		}
		timezoneName = configured
	}
	location, err := time.LoadLocation(timezoneName)
	if err != nil {
		app.recordStartupError(fmt.Errorf("加载 app.default_timezone %q 失败: %w", timezoneName, err))
		return
	}
	app.locationMu.Lock()
	app.location = location
	app.locationMu.Unlock()
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

// validateConsoleApplicationConfig 让控制台配置在应用启动时就进入统一错误链。
func (app *App) validateConsoleApplicationConfig() {
	if app == nil || app.config == nil {
		return
	}
	values := app.config.GetMap("console")
	for key := range values {
		switch key {
		case "commands", "name", "version", "user", "auto_path":
		default:
			app.recordStartupError(fmt.Errorf("console 配置包含未知字段 %q", key))
		}
	}
	if raw, exists := values["commands"]; exists {
		commands, ok := raw.([]interface{})
		if !ok {
			app.recordStartupError(fmt.Errorf("console.commands 必须是字符串列表"))
		} else {
			for index, rawCommand := range commands {
				command, valid := rawCommand.(string)
				if !valid || strings.TrimSpace(command) == "" || hasApplicationControl(command) {
					app.recordStartupError(fmt.Errorf("console.commands 第 %d 项必须是安全的非空字符串", index+1))
				}
			}
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
