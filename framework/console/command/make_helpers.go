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
	"thinkgo/framework/console"
)

// normalizedGeneratorInput 校验生成器执行上下文并规范化类型名。
func normalizedGeneratorInput(app *framework.App, input *console.Input, output *console.Output, suffix string) (string, error) {
	if app == nil {
		return "", framework.ErrNilApplication
	}
	if input == nil {
		return "", fmt.Errorf("命令输入不能为空")
	}
	if output == nil {
		return "", console.ErrInvalidOutput
	}
	return normalizeGeneratorName(input.GetArgument(0), suffix)
}

// writeGeneratedAppSource 校验应用实例后写入生成源码。
func writeGeneratedAppSource(app *framework.App, relativeDir, filename string, source []byte) error {
	if app == nil {
		return framework.ErrNilApplication
	}
	return writeGeneratedSource(app.BasePath, relativeDir, filename, source)
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
