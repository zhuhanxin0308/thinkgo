package framework

import (
	"errors"
	"fmt"
)

// appCookieProvider 负责在配置完成后装配应用 Cookie 工厂。
type appCookieProvider struct{}

// Register 保留 Provider 注册阶段，Cookie 工厂必须等待最终配置完成后创建。
func (provider *appCookieProvider) Register(app *App) error {
	return nil
}

// Initialize 根据配置创建 Cookie 工厂，并在失败时安装安全退化对象。
func (provider *appCookieProvider) Initialize(app *App) error {
	if app == nil {
		return ErrNilApplication
	}
	if app.config == nil {
		return errors.New("应用配置实例不能为空")
	}
	if app.container == nil {
		return errors.New("应用容器实例不能为空")
	}

	configuredCookie, err := createAppCookie(app.config.GetMap("cookie"))
	if err != nil {
		app.cookie = fallbackAppCookie()
		return errors.Join(
			fmt.Errorf("初始化 Cookie 失败: %w", err),
			wrapServiceBindingError(serviceKeyCookie, app.Instance(serviceKeyCookie, app.cookie)),
		)
	}
	app.cookie = configuredCookie
	return wrapServiceBindingError(serviceKeyCookie, app.Instance(serviceKeyCookie, app.cookie))
}

// Boot 不在 Provider 启动阶段重复创建 Cookie 工厂。
func (provider *appCookieProvider) Boot(app *App) error {
	return nil
}
