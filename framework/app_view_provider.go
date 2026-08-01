package framework

import (
	"errors"
	"fmt"

	"thinkgo/framework/view"
	"thinkgo/framework/view/driver"
)

// appViewProvider 负责在语言服务完成后装配模板视图。
type appViewProvider struct{}

// Register 保留 Provider 注册阶段，视图驱动必须等待最终配置和语言服务完成后安装。
func (provider *appViewProvider) Register(app *App) error {
	return nil
}

// Initialize 初始化视图配置、驱动和模板函数。
func (provider *appViewProvider) Initialize(app *App) error {
	if app == nil {
		return ErrNilApplication
	}
	if app.config == nil {
		return errors.New("应用配置实例不能为空")
	}
	if app.container == nil {
		return errors.New("应用容器实例不能为空")
	}

	viewConfig := app.config.GetMap("view")
	app.normalizeViewConfig(viewConfig)
	app.view = view.NewView(nil, viewConfig)
	initializeErrors := make([]error, 0, 2)
	if err := app.view.SetDriver(driver.NewGoTemplate()); err != nil {
		initializeErrors = append(initializeErrors, fmt.Errorf("初始化视图驱动失败: %w", err))
	} else {
		// 仅在驱动成功安装后注册函数，避免同一根因产生重复启动错误。
		if err := app.view.SetFuncMap(map[string]interface{}{
			"lang": func(key string) string {
				return app.lang.Get(key, nil, "")
			},
		}); err != nil {
			initializeErrors = append(initializeErrors, fmt.Errorf("注册视图函数失败: %w", err))
		}
	}

	app.Instance(serviceKeyView, app.view)
	return errors.Join(initializeErrors...)
}

// Boot 不在 Provider 启动阶段重复创建视图驱动。
func (provider *appViewProvider) Boot(app *App) error {
	return nil
}
