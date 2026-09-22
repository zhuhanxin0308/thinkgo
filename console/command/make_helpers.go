package command

import (
	"errors"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

// resolveApplicationConfig 通过应用服务边界解析配置，避免命令包依赖 App 内部字段。
func resolveApplicationConfig(app *framework.App) (*config.Config, error) {
	return framework.ResolveServiceAs[*config.Config](app, framework.ServiceConfig)
}

// resolveApplicationCache 通过应用服务边界解析缓存，命令只依赖缓存契约。
func resolveApplicationCache(app *framework.App) (*cache.Cache, error) {
	return framework.ResolveServiceAs[*cache.Cache](app, framework.ServiceCache)
}

// resolveApplicationRoute 通过应用服务边界解析路由器，避免暴露 App 路由字段。
func resolveApplicationRoute(app *framework.App) (*route.Router, error) {
	return framework.ResolveServiceAs[*route.Router](app, framework.ServiceRoute)
}

type generatorTarget struct {
	application string
	name        string
	directory   string
}

// addApplicationSelectionOption 为应用级命令提供一致的 --app/-a 选择入口。
func addApplicationSelectionOption(command *console.Command) {
	if command != nil {
		command.AddOption("app", "a", "Target compiled application", "")
	}
}

// normalizedGeneratorInput 保留原有辅助函数契约；应用选择由
// normalizedGeneratorTarget 使用 ThinkPHP 的 应用名@类名 语法完成。
func normalizedGeneratorInput(command *console.Command, input *console.Input, output *console.Output, suffix string) (string, error) {
	target, err := normalizedGeneratorTarget(command, input, output, suffix)
	if err != nil {
		return "", err
	}
	return target.name, nil
}

// normalizedGeneratorTarget 校验生成器上下文并解析 ThinkPHP 多应用类名。
func normalizedGeneratorTarget(command *console.Command, input *console.Input, output *console.Output, suffix string) (generatorTarget, error) {
	if command == nil {
		return generatorTarget{}, framework.ErrNilApplication
	}
	if input == nil {
		return generatorTarget{}, fmt.Errorf("命令输入不能为空")
	}
	if output == nil {
		return generatorTarget{}, console.ErrInvalidOutput
	}
	if command.App == nil {
		return generatorTarget{}, framework.ErrNilApplication
	}
	rawName := strings.TrimSpace(input.GetArgument(0))
	if strings.Count(rawName, "@") > 1 {
		return generatorTarget{}, fmt.Errorf("name must use application@class format")
	}
	applicationName := defaultGeneratorApplication(command.App)
	optionApplication := strings.TrimSpace(input.GetOption("app"))
	className := rawName
	if separator := strings.IndexByte(rawName, '@'); separator >= 0 {
		explicitApplication := strings.TrimSpace(rawName[:separator])
		if optionApplication != "" && optionApplication != explicitApplication {
			return generatorTarget{}, fmt.Errorf("--app 与 application@class 中的应用选择冲突")
		}
		applicationName = explicitApplication
		className = strings.TrimSpace(rawName[separator+1:])
	} else if optionApplication != "" {
		applicationName = optionApplication
	}
	if err := validateNativeApplicationPackageName(applicationName); err != nil {
		return generatorTarget{}, err
	}
	directory, className, err := splitGeneratorClassPath(className)
	if err != nil {
		return generatorTarget{}, err
	}
	normalizedName, err := normalizeGeneratorName(className, suffix)
	if err != nil {
		return generatorTarget{}, err
	}
	return generatorTarget{application: applicationName, name: normalizedName, directory: directory}, nil
}

func defaultGeneratorApplication(app *framework.App) string {
	if app != nil {
		configuration := app.ProjectApplicationConfig()
		if name, ok := configuration["default_app"].(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
		if app.Config() != nil {
			if name := strings.TrimSpace(app.Config().GetString("app.default_app")); name != "" {
				return name
			}
		}
	}
	return "index"
}

// writeGeneratedAppSource 校验应用实例后写入生成源码。
func writeGeneratedAppSource(app *framework.App, target generatorTarget, relativeDir, filename string, source []byte) error {
	if app == nil {
		return framework.ErrNilApplication
	}
	if err := validateNativeApplicationPackageName(target.application); err != nil {
		return err
	}
	if target.directory != "" {
		var err error
		source, err = generatorPackageSource(source, filepath.Base(target.directory))
		if err != nil {
			return err
		}
	}
	return writeGeneratedSource(app.BasePath, filepath.Join("app", target.application, relativeDir, target.directory), filename, source)
}

// writeAndRefreshGeneratedControllerSource 创建控制器后立即刷新编译发现文件；
// 刷新失败时回滚本次源码，避免生成无法被路由访问的控制器。
func writeAndRefreshGeneratedControllerSource(app *framework.App, target generatorTarget, filename string, source []byte) error {
	return writeAndRefreshGeneratedApplicationSource(app, target, "controller", filename, source)
}

// writeAndRefreshGeneratedApplicationSource 创建可自动发现的业务类型后刷新
// 应用装配入口；刷新失败时回滚源码，避免留下无法被框架解析的类型。
func writeAndRefreshGeneratedApplicationSource(app *framework.App, target generatorTarget, layer, filename string, source []byte) error {
	if err := writeGeneratedAppSource(app, target, layer, filename, source); err != nil {
		return err
	}
	if err := RefreshControllerDiscovery(app); err != nil {
		return errors.Join(err, removeGeneratedAppSource(app, filepath.Join("app", target.application, layer, target.directory), filename))
	}
	return nil
}

func removeGeneratedAppSource(app *framework.App, relativeDir, filename string) error {
	if app == nil {
		return framework.ErrNilApplication
	}
	if filepath.IsAbs(relativeDir) || filename == "" || filepath.Base(filename) != filename || filepath.Ext(filename) != ".go" {
		return fmt.Errorf("生成回滚路径非法: dir=%q file=%q", relativeDir, filename)
	}
	root, err := os.OpenRoot(app.BasePath)
	if err != nil {
		return fmt.Errorf("打开项目根目录失败: %w", err)
	}
	defer root.Close()
	if err := root.Remove(filepath.Join(relativeDir, filename)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("回滚生成文件失败: %w", err)
	}
	return nil
}

func applicationRelativeDirectory(app *framework.App) (string, error) {
	if app == nil {
		return "", framework.ErrNilApplication
	}
	basePath := strings.TrimSpace(app.BasePath)
	if basePath == "" {
		return "", fmt.Errorf("应用根目录不能为空")
	}
	baseAbsolute, err := filepath.Abs(basePath)
	if err != nil {
		return "", fmt.Errorf("解析应用根目录失败: %w", err)
	}
	var configuration *config.Config
	if app.Has(string(framework.ServiceConfig)) {
		var resolveErr error
		configuration, resolveErr = resolveApplicationConfig(app)
		if resolveErr != nil {
			return "", fmt.Errorf("解析应用配置失败: %w", resolveErr)
		}
	}
	if configuration != nil {
		configuredPath := strings.TrimSpace(configuration.GetString("console.auto_path"))
		if configuredPath != "" {
			candidate := configuredPath
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(baseAbsolute, candidate)
			}
			candidate, err = filepath.Abs(filepath.Clean(candidate))
			if err != nil {
				return "", fmt.Errorf("解析 console.auto_path 失败: %w", err)
			}
			relative, relErr := filepath.Rel(baseAbsolute, candidate)
			if relErr != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("console.auto_path 必须位于项目根目录下: %s", configuredPath)
			}
			return relative, nil
		}
	}
	applicationPath := strings.TrimSpace(app.ApplicationPath)
	if applicationPath == "" {
		applicationPath = filepath.Join(baseAbsolute, "app")
	}
	applicationAbsolute, err := filepath.Abs(applicationPath)
	if err != nil {
		return "", fmt.Errorf("解析应用路径失败: %w", err)
	}
	relative, err := filepath.Rel(baseAbsolute, applicationAbsolute)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("应用路径必须位于项目根目录下: %s", applicationPath)
	}
	return relative, nil
}

// writeGeneratedSource 在应用根目录约束内格式化并独占创建 Go 源码。
// os.Root 阻止路径穿越和指向根目录外的符号链接，O_EXCL 消除检查后覆盖竞态。
func writeGeneratedSource(basePath, relativeDir, filename string, source []byte) (returnErr error) {
	if strings.TrimSpace(basePath) == "" {
		return fmt.Errorf("应用根目录不能为空")
	}
	if filepath.IsAbs(relativeDir) || filename == "" || filepath.Base(filename) != filename || filepath.Ext(filename) != ".go" {
		return fmt.Errorf("生成路径非法: dir=%q file=%q", relativeDir, filename)
	}
	formatted, err := format.Source(source)
	if err != nil {
		return fmt.Errorf("生成的 Go 源码格式化失败: %w", err)
	}

	root, err := os.OpenRoot(basePath)
	if err != nil {
		return fmt.Errorf("打开应用根目录失败: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	if err := root.MkdirAll(relativeDir, 0o755); err != nil {
		return fmt.Errorf("创建生成目录失败: %w", err)
	}

	relativePath := filepath.Join(relativeDir, filename)
	file, err := root.OpenFile(relativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("创建生成文件失败: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			returnErr = errors.Join(returnErr, root.Remove(relativePath))
		}
	}()

	written, writeErr := file.Write(formatted)
	if writeErr == nil && written != len(formatted) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return errors.Join(fmt.Errorf("写入生成文件失败: %w", writeErr), file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("同步生成文件失败: %w", err), file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭生成文件失败: %w", err)
	}
	complete = true
	return nil
}

// normalizeGeneratorName 把命令输入转换成安全的 Go 导出类型名，并拒绝路径穿越。
func normalizeGeneratorName(raw string, suffix string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("name is required")
	}
	if strings.ContainsAny(trimmed, `/\.`) {
		return "", fmt.Errorf("name must not contain path separators or dots")
	}

	parts := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == '_' || r == '-' || unicode.IsSpace(r)
	})
	if len(parts) == 0 {
		return "", fmt.Errorf("name is invalid")
	}

	builder := strings.Builder{}
	for _, part := range parts {
		if part == "" {
			continue
		}
		for _, r := range part {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				return "", fmt.Errorf("name contains invalid character %q", r)
			}
		}
		normalizedPart := part
		if len(parts) > 1 || strings.ContainsAny(trimmed, "_- ") {
			normalizedPart = strings.ToLower(part)
		}
		runes := []rune(normalizedPart)
		runes[0] = unicode.ToUpper(runes[0])
		builder.WriteString(string(runes))
	}

	name := builder.String()
	if name == "" || !isExportedIdentifier(name) {
		return "", fmt.Errorf("name must produce an exported Go identifier")
	}
	if suffix != "" && !strings.HasSuffix(name, suffix) {
		name += suffix
	}
	return name, nil
}

func isExportedIdentifier(name string) bool {
	for index, r := range name {
		if index == 0 {
			if !unicode.IsLetter(r) || !unicode.IsUpper(r) {
				return false
			}
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func lowerGoFilename(typeName string) string {
	return strings.ToLower(typeName) + ".go"
}
