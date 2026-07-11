package command

import (
	"fmt"
	"strings"
	"unicode"
)

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
