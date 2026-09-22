package framework

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var errInvalidApplicationStoragePath = errors.New("应用存储路径非法")

// GetRootPath 返回项目根目录，对应 ThinkPHP App::getRootPath。
func (app *App) GetRootPath() string {
	return app.projectBasePath()
}

// GetBasePath 返回应用基础目录，对应 ThinkPHP App::getBasePath。
func (app *App) GetBasePath() string {
	return filepath.Join(app.projectBasePath(), "app")
}

// GetAppPath 返回当前应用目录，对应 ThinkPHP App::getAppPath。
func (app *App) GetAppPath() string {
	return app.applicationDirectory()
}

// SetAppPath 设置当前应用目录，对应 ThinkPHP App::setAppPath。
func (app *App) SetAppPath(path string) error {
	return app.setAppPath(path, true)
}

func (app *App) setAppPath(path string, explicit bool) error {
	if app == nil {
		return ErrNilApplication
	}
	normalized, err := app.normalizeProjectPath(path, "应用目录")
	if err != nil {
		return err
	}
	app.ApplicationPath = normalized
	if explicit {
		app.nativeApplicationPath = normalized
	}
	return nil
}

// GetRuntimePath 返回应用运行时目录，对应 ThinkPHP App::getRuntimePath。
func (app *App) GetRuntimePath() string {
	return app.ApplicationRuntimePath()
}

// SetRuntimePath 设置应用运行时目录，对应 ThinkPHP App::setRuntimePath。
func (app *App) SetRuntimePath(path string) error {
	return app.setRuntimePath(path, true)
}

func (app *App) setRuntimePath(path string, explicit bool) error {
	if app == nil {
		return ErrNilApplication
	}
	normalized, err := app.normalizeProjectPath(path, "运行时目录")
	if err != nil {
		return err
	}
	app.RuntimePath = normalized
	if explicit {
		app.nativeRuntimeBasePath = normalized
	}
	return nil
}

// GetConfigPath 返回项目配置目录，对应 ThinkPHP App::getConfigPath。
func (app *App) GetConfigPath() string {
	return filepath.Join(app.projectBasePath(), "config")
}

// GetRoutePath 返回当前应用路由目录；原生多应用使用 app/<name>/route，
// 未注册原生应用时保留 ThinkPHP 单应用的根 route 目录。
func (app *App) GetRoutePath() string {
	if app != nil && app.hasNativeApplication() {
		return filepath.Join(app.nativeApplicationDirectory(), "route")
	}
	return filepath.Join(app.projectBasePath(), "route")
}

// ApplicationLangPath 返回当前应用的语言资源目录。
func (app *App) ApplicationLangPath() string {
	return filepath.Join(app.applicationDirectory(), "lang")
}

// ApplicationViewPath 返回当前应用的视图资源目录。
// 原生多应用使用 app/<name>/view，未注册原生应用时保留根 view 兼容行为。
func (app *App) ApplicationViewPath() string {
	if app != nil && app.hasNativeApplication() {
		return filepath.Join(app.nativeApplicationDirectory(), "view")
	}
	return filepath.Join(app.projectBasePath(), "view")
}

func (app *App) nativeApplicationDirectory() string {
	if app == nil {
		return ""
	}
	name := app.CurrentApplicationName()
	if name != "" {
		return app.configuredNativeApplicationPath(name)
	}
	return app.applicationDirectory()
}

// ApplicationRuntimePath 返回当前应用的运行时目录。
func (app *App) ApplicationRuntimePath() string {
	if app == nil {
		return ""
	}
	if runtimePath := strings.TrimSpace(app.RuntimePath); runtimePath != "" {
		return filepath.Clean(runtimePath)
	}
	return filepath.Join(app.projectBasePath(), "runtime")
}

// RuntimeLogPath 返回当前应用的日志目录。
func (app *App) RuntimeLogPath() string {
	return filepath.Join(app.ApplicationRuntimePath(), "log")
}

// RuntimeCachePath 返回当前应用的文件缓存目录。
func (app *App) RuntimeCachePath() string {
	return filepath.Join(app.ApplicationRuntimePath(), "cache")
}

// RuntimeSessionPath 返回当前应用的文件会话目录。
func (app *App) RuntimeSessionPath() string {
	return filepath.Join(app.ApplicationRuntimePath(), "session")
}

// ProjectPublicPath 返回宿主项目公共静态资源目录。
func (app *App) ProjectPublicPath() string {
	if app != nil {
		if configured := strings.TrimSpace(os.Getenv("APP_PUBLIC_PATH")); configured != "" {
			if filepath.IsAbs(configured) {
				return filepath.Clean(configured)
			}
			return filepath.Join(app.projectBasePath(), configured)
		}
		if app.config != nil {
			if configured := strings.TrimSpace(app.config.GetString("app.public_path")); configured != "" {
				if filepath.IsAbs(configured) {
					return filepath.Clean(configured)
				}
				return filepath.Join(app.projectBasePath(), configured)
			}
		}
	}
	return filepath.Join(app.projectBasePath(), "public")
}

// ExceptionTemplatePath 返回宿主项目异常模板目录。
func (app *App) ExceptionTemplatePath() string {
	return filepath.Join(app.projectBasePath(), "framework", "exception", "tpl")
}

func (app *App) projectBasePath() string {
	if app == nil {
		return ""
	}
	return filepath.Clean(strings.TrimSpace(app.BasePath))
}

func (app *App) applicationDirectory() string {
	if app == nil {
		return ""
	}
	if applicationPath := strings.TrimSpace(app.ApplicationPath); applicationPath != "" {
		return filepath.Clean(applicationPath)
	}
	return filepath.Join(app.projectBasePath(), "app")
}

// normalizeProjectPath 规范开发者显式设置的应用路径，保留 ThinkPHP 可切换路径的能力，
// 同时拒绝空值、控制字符和无法解析的路径。
func (app *App) normalizeProjectPath(path string, label string) (string, error) {
	if path == "" || strings.TrimSpace(path) != path || !utf8.ValidString(path) {
		return "", fmt.Errorf("%s必须是有效的非空路径", label)
	}
	for _, character := range path {
		if character < ' ' || character == 0x7f {
			return "", fmt.Errorf("%s不能包含控制字符", label)
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(app.projectBasePath(), path)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析%s失败: %w", label, err)
	}
	return filepath.Clean(absolutePath), nil
}

func (app *App) resolveStoragePath(configuredPath string) (string, error) {
	if app == nil {
		return "", ErrNilApplication
	}
	if configuredPath == "" || strings.TrimSpace(configuredPath) != configuredPath ||
		!utf8.ValidString(configuredPath) || containsAppStoragePathControl(configuredPath) {
		return "", fmt.Errorf("%w: 路径必须是不含首尾空白和控制字符的有效字符串", errInvalidApplicationStoragePath)
	}
	if filepath.IsAbs(configuredPath) || filepath.VolumeName(configuredPath) != "" {
		return normalizeAppStoragePath(app.ApplicationRuntimePath(), configuredPath)
	}
	for _, segment := range strings.FieldsFunc(configuredPath, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if segment == ".." {
			return "", fmt.Errorf("%w: 相对路径不能包含父级目录", errInvalidApplicationStoragePath)
		}
	}

	cleaned := filepath.Clean(filepath.FromSlash(configuredPath))
	cleanedSlash := filepath.ToSlash(cleaned)
	if cleanedSlash == "runtime" {
		return app.ApplicationRuntimePath(), nil
	}
	cleanedSlash = strings.TrimPrefix(cleanedSlash, "runtime/")
	if cleanedSlash == "" || cleanedSlash == "." {
		return app.ApplicationRuntimePath(), nil
	}
	return normalizeAppStoragePath(app.ApplicationRuntimePath(), cleanedSlash)
}

func (app *App) normalizeViewConfig(viewConfig map[string]interface{}) {
	if app == nil {
		return
	}
	viewPath, ok := viewConfig["view_path"].(string)
	if !ok {
		return
	}
	viewPath = strings.TrimSpace(viewPath)
	viewConfig["view_path"] = viewPath
	if viewPath == "" || filepath.IsAbs(viewPath) {
		return
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(viewPath)))
	if cleaned == "view" || cleaned == "app/view" || cleaned == "app" {
		viewConfig["view_path"] = app.ApplicationViewPath()
		return
	}
	viewConfig["view_path"] = filepath.Join(app.projectBasePath(), cleaned)
}
