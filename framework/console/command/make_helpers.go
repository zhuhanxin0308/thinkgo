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

	"thinkgo/framework"
	"thinkgo/framework/cache"
	"thinkgo/framework/config"
	"thinkgo/framework/console"
	"thinkgo/framework/route"
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

// normalizedGeneratorInput 校验应用选择、生成器执行上下文并规范化类型名。
func normalizedGeneratorInput(command *console.Command, input *console.Input, output *console.Output, suffix string) (string, error) {
	if command == nil {
		return "", framework.ErrNilApplication
	}
	if input == nil {
		return "", fmt.Errorf("命令输入不能为空")
	}
	if output == nil {
		return "", console.ErrInvalidOutput
	}
	if err := command.SelectApplication(input); err != nil {
		return "", err
	}
	if command.App == nil {
		return "", framework.ErrNilApplication
	}
	return normalizeGeneratorName(input.GetArgument(0), suffix)
}

// writeGeneratedAppSource 校验应用实例后写入生成源码。
func writeGeneratedAppSource(app *framework.App, relativeDir, filename string, source []byte) error {
	if app == nil {
		return framework.ErrNilApplication
	}
	applicationDir, err := applicationRelativeDirectory(app)
	if err != nil {
		return err
	}
	return writeGeneratedSource(app.BasePath, filepath.Join(applicationDir, relativeDir), filename, source)
}

// writeAndRegisterGeneratedAppSource 在注册失败时回滚刚创建的源码，避免留下无法装配的孤儿组件。
func writeAndRegisterGeneratedAppSource(app *framework.App, relativeDir, filename string, source []byte, kind applicationRegistrationKind, typeName string) error {
	if err := writeGeneratedAppSource(app, relativeDir, filename, source); err != nil {
		return err
	}
	if err := updateApplicationRegistration(app, kind, typeName); err != nil {
		applicationDir, directoryErr := applicationRelativeDirectory(app)
		if directoryErr != nil {
			return errors.Join(err, directoryErr)
		}
		return errors.Join(err, removeGeneratedAppSource(app, filepath.Join(applicationDir, relativeDir), filename))
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
		applicationName := strings.TrimSpace(app.ApplicationName)
		if applicationName == "" {
			applicationName = "index"
		}
		applicationPath = filepath.Join(baseAbsolute, "app", applicationName)
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
