package framework

import "fmt"

// applyBooleanEnvironmentOverride 严格解析布尔环境变量，存在但非法时记录启动错误。
func (app *App) applyBooleanEnvironmentOverride(environmentKey string, configPath string) {
	if _, exists := app.env.Lookup(environmentKey); !exists {
		return
	}
	value, err := app.env.GetBool(environmentKey)
	if err != nil {
		app.recordStartupError(err)
		return
	}
	app.setConfigValue(configPath, value)
}

// applyStringEnvironmentOverride 使用显式环境变量覆盖字符串配置，保留空字符串的明确语义。
func (app *App) applyStringEnvironmentOverride(environmentKey string, configPath string) {
	if app == nil || app.env == nil || app.config == nil {
		return
	}
	value, exists := app.env.Lookup(environmentKey)
	if !exists {
		return
	}
	app.setConfigValue(configPath, value)
}

// setConfigValue 写入框架内部配置，并把不应发生的路径错误纳入启动错误。
func (app *App) setConfigValue(configPath string, value interface{}) {
	if app == nil || app.config == nil {
		return
	}
	if err := app.config.Set(configPath, value); err != nil {
		app.recordStartupError(fmt.Errorf("设置配置 %q 失败: %w", configPath, err))
	}
}
