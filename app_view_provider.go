package framework

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/view"
	"github.com/zhuhanxin0308/thinkgo/v3/view/driver"
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

	viewConfig, err := app.resolveViewDriverConfig(app.config.GetMap("view"))
	if err != nil {
		return err
	}
	app.view = view.NewView(nil, viewConfig)
	initializeErrors := make([]error, 0, 2)
	if err = app.view.SetDriver(driver.NewGoTemplate()); err != nil {
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

	if err := app.Instance(serviceKeyView, app.view); err != nil {
		initializeErrors = append(initializeErrors, fmt.Errorf("绑定视图服务失败: %w", err))
	}
	return errors.Join(initializeErrors...)
}

// Boot 不在 Provider 启动阶段重复创建视图驱动。
func (provider *appViewProvider) Boot(app *App) error {
	return nil
}

// resolveViewDriverConfig 把 ThinkPHP 的 view.php 配置转换为模板驱动需要的
// 运行参数；应用配置本身仍保留 ThinkPHP 键名，业务代码读取配置时不会看到内部形态。
func (app *App) resolveViewDriverConfig(configuration map[string]interface{}) (map[string]interface{}, error) {
	if app == nil {
		return nil, ErrNilApplication
	}
	allowed := map[string]bool{
		"type": true, "auto_rule": true, "view_dir_name": true,
		"view_path": true, "view_suffix": true, "view_depr": true,
		"tpl_begin": true, "tpl_end": true, "taglib_begin": true,
		"taglib_end": true, "tpl_cache": true, "cache": true,
	}
	unknown := make([]string, 0)
	for key := range configuration {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("view 包含未知字段 %s", strings.Join(unknown, ", "))
	}

	engineType := "Think"
	if raw, exists := configuration["type"]; exists {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("view.type 必须是非空字符串")
		}
		engineType = strings.TrimSpace(value)
	}
	if !strings.EqualFold(engineType, "Think") && !strings.EqualFold(engineType, "Go") {
		return nil, fmt.Errorf("view.type %q 未注册", engineType)
	}

	if raw, exists := configuration["auto_rule"]; exists {
		value, parseErr := readConfigIntValue(raw)
		if parseErr != nil || value < 1 || value > 3 {
			return nil, fmt.Errorf("view.auto_rule 必须是 1、2 或 3")
		}
	}
	for _, key := range []string{"tpl_begin", "tpl_end", "taglib_begin", "taglib_end"} {
		if raw, exists := configuration[key]; exists {
			value, ok := raw.(string)
			if !ok || value == "" || strings.ContainsAny(value, "\x00\r\n") {
				return nil, fmt.Errorf("view.%s 必须是不含控制字符的非空字符串", key)
			}
		}
	}

	viewDirectoryName := "view"
	if raw, exists := configuration["view_dir_name"]; exists {
		value, ok := raw.(string)
		if !ok || !isSafeViewDirectoryName(value) {
			return nil, fmt.Errorf("view.view_dir_name 必须是单个安全目录名")
		}
		viewDirectoryName = value
	}
	if raw, exists := configuration["view_depr"]; exists {
		value, ok := raw.(string)
		if !ok || value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("view.view_depr 必须是不含控制字符的非空字符串")
		}
	}

	viewPath, err := app.resolveConfiguredViewPath(configuration, viewDirectoryName)
	if err != nil {
		return nil, err
	}
	viewSuffix := "html"
	if raw, exists := configuration["view_suffix"]; exists {
		value, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("view.view_suffix 必须是字符串")
		}
		viewSuffix = value
	}
	cacheEnabled, err := readViewCacheOption(configuration)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"view_path":   viewPath,
		"view_suffix": viewSuffix,
		"cache":       cacheEnabled,
	}, nil
}

func (app *App) resolveConfiguredViewPath(configuration map[string]interface{}, directoryName string) (string, error) {
	if raw, exists := configuration["view_path"]; exists {
		configured, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("view.view_path 必须是字符串")
		}
		configured = strings.TrimSpace(configured)
		if configured != "" {
			if !filepath.IsAbs(configured) {
				configured = filepath.Join(app.projectBasePath(), filepath.FromSlash(configured))
			}
			return filepath.Clean(configured), nil
		}
	}

	candidates := []string{
		filepath.Join(app.GetBasePath(), directoryName),
		filepath.Join(app.GetRootPath(), directoryName),
	}
	if app.hasNativeApplication() {
		applicationName := app.CurrentApplicationName()
		candidates = []string{
			app.ApplicationViewPath(),
			filepath.Join(app.GetBasePath(), directoryName, applicationName),
			filepath.Join(app.GetRootPath(), directoryName, applicationName),
		}
	}
	for _, candidate := range candidates {
		info, statErr := os.Stat(candidate)
		if statErr == nil && info.IsDir() {
			return filepath.Clean(candidate), nil
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("读取视图目录失败: %w", statErr)
		}
	}
	// ThinkPHP 会在首次渲染时报告模板路径不存在；驱动仍需要稳定的根目录。
	if app.hasNativeApplication() {
		return app.ApplicationViewPath(), nil
	}
	return filepath.Join(app.GetRootPath(), directoryName), nil
}

func readViewCacheOption(configuration map[string]interface{}) (bool, error) {
	cacheEnabled := true
	for _, key := range []string{"tpl_cache", "cache"} {
		raw, exists := configuration[key]
		if !exists {
			continue
		}
		value, ok := raw.(bool)
		if !ok {
			return false, fmt.Errorf("view.%s 必须是布尔值", key)
		}
		cacheEnabled = value
	}
	return cacheEnabled, nil
}

func isSafeViewDirectoryName(name string) bool {
	if name == "" || strings.TrimSpace(name) != name || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\r\n") {
		return false
	}
	return filepath.Base(name) == name
}
