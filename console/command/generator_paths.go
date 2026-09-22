package command

import (
	"bytes"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

// splitGeneratorClassPath 将 ThinkPHP 分层类名映射到 Go 子包，始终逐段校验原始路径。
func splitGeneratorClassPath(value string) (string, string, error) {
	parts := strings.Split(strings.ReplaceAll(value, `\`, "/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.TrimSpace(part) != part {
			return "", "", fmt.Errorf("组件路径包含空段或目录穿越")
		}
	}
	for _, directory := range parts[:len(parts)-1] {
		if err := validateNativeApplicationPackageName(directory); err != nil {
			return "", "", err
		}
	}
	return filepath.Join(append([]string{""}, parts[:len(parts)-1]...)...), parts[len(parts)-1], nil
}

// generatorPackageSource 通过 Go 语法树设置生成文件的真实包名，不替换注释或业务字符串。
func generatorPackageSource(source []byte, packageName string) ([]byte, error) {
	if err := validateNativeApplicationPackageName(packageName); err != nil {
		return nil, err
	}
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, "generated.go", source, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("解析生成模板失败: %w", err)
	}
	parsed.Name.Name = packageName
	var output bytes.Buffer
	if err := format.Node(&output, files, parsed); err != nil {
		return nil, fmt.Errorf("格式化生成模板失败: %w", err)
	}
	return output.Bytes(), nil
}
