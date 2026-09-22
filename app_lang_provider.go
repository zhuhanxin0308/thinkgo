package framework

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/zhuhanxin0308/thinkgo/v3/lang"
)

// appLangProvider 负责在配置完成后装配多语言配置和语言包。
type appLangProvider struct{}

// Register 保留 Provider 注册阶段，多语言配置必须等待最终配置完成后读取。
func (provider *appLangProvider) Register(app *App) error {
	return nil
}

// Initialize 初始化多语言配置并加载应用语言包。
func (provider *appLangProvider) Initialize(app *App) error {
	if app == nil {
		return ErrNilApplication
	}
	if app.config == nil {
		return errors.New("应用配置实例不能为空")
	}
	if app.container == nil {
		return errors.New("应用容器实例不能为空")
	}
	if app.lang == nil {
		app.lang = lang.NewLang()
	}

	langConfig := app.config.GetMap("lang")
	// 兼容旧配置：如果 lang 配置没有 default_lang，从 app 配置读取。
	if _, ok := langConfig["default_lang"]; !ok {
		langConfig["default_lang"] = app.config.Get("app.default_lang", "zh-cn")
	}

	initializeErrors := make([]error, 0, 2)
	if err := app.lang.Init(langConfig); err != nil {
		initializeErrors = append(initializeErrors, fmt.Errorf("初始化多语言配置失败: %w", err))
	} else if app.hasNativeApplication() {
		// ThinkPHP 先加载根 app/lang，再加载 app/<name>/lang；应用翻译优先。
		if err := app.lang.LoadAll(filepath.Join(app.GetBasePath(), "lang")); err != nil {
			initializeErrors = append(initializeErrors, fmt.Errorf("加载全局语言文件失败: %w", err))
		} else if err := app.lang.MergeAll(app.ApplicationLangPath()); err != nil {
			initializeErrors = append(initializeErrors, fmt.Errorf("加载应用语言文件失败: %w", err))
		}
	} else if err := app.lang.LoadAll(app.ApplicationLangPath()); err != nil {
		initializeErrors = append(initializeErrors, fmt.Errorf("加载语言文件失败: %w", err))
	}

	if err := app.Instance(serviceKeyLang, app.lang); err != nil {
		initializeErrors = append(initializeErrors, fmt.Errorf("绑定多语言服务失败: %w", err))
	}
	return errors.Join(initializeErrors...)
}

// Boot 不在 Provider 启动阶段重复加载语言包。
func (provider *appLangProvider) Boot(app *App) error {
	return nil
}
