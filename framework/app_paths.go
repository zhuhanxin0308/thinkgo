package framework

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const defaultApplicationName = "index"

var errInvalidApplicationStoragePath = errors.New("应用存储路径非法")

// ApplicationConfigPath 返回当前应用的配置目录。
func (app *App) ApplicationConfigPath() string {
	return filepath.Join(app.applicationDirectory(), "config")
}

// ApplicationLangPath 返回当前应用的语言资源目录。
func (app *App) ApplicationLangPath() string {
	return filepath.Join(app.applicationDirectory(), "lang")
}

// ApplicationViewPath 返回当前应用的视图资源目录。
func (app *App) ApplicationViewPath() string {
	return filepath.Join(app.applicationDirectory(), "view")
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
	return filepath.Join(app.projectBasePath(), "public")
}

// ExceptionTemplatePath 返回宿主项目异常模板目录。
func (app *App) ExceptionTemplatePath() string {
	return filepath.Join(app.projectBasePath(), "framework", "exception", "tpl")
}

func (app *App) loadApplicationConfig() error {
	if app == nil || app.config == nil {
		return ErrNilApplication
	}
	configPath := app.ApplicationConfigPath()
	if configPath == "" {
		return nil
	}
	info, err := os.Stat(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("应用配置路径不是目录: %s", configPath)
	}
	return app.config.LoadAll(configPath)
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
	name := strings.TrimSpace(app.ApplicationName)
	if name == "" {
		name = defaultApplicationName
	}
	return filepath.Join(app.projectBasePath(), "app", name)
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
	if cleaned == "app/view" || cleaned == "app" {
		viewConfig["view_path"] = app.ApplicationViewPath()
		return
	}
	viewConfig["view_path"] = filepath.Join(app.applicationDirectory(), cleaned)
}
