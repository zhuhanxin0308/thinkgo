// Package redact 包含框架不同输出入口共用的脱敏规则。
package redact

import (
	"strings"
	"unicode"
)

var compactCredentialKey = strings.NewReplacer("-", "", "_", "", ".", "", ":", "", "/", "")

// CredentialAlias 匹配常见凭据别名，短别名按分隔符与驼峰字段边界判断。
func CredentialAlias(key string) bool {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return false
	}
	compact := compactCredentialKey.Replace(strings.ToLower(trimmed))
	switch compact {
	case "key", "xkey", "xauth", "pass", "xpass":
		return true
	}
	if strings.Contains(compact, "passphrase") || strings.Contains(compact, "dbpass") ||
		strings.Contains(compact, "masterkey") || strings.Contains(compact, "credential") {
		return true
	}
	tokens := splitCredentialTokens(trimmed)
	if len(tokens) == 0 {
		return false
	}
	// pass 和 auth 也用于指标和状态名称，仅按完整的凭据末段匹配。
	switch tokens[len(tokens)-1] {
	case "pass", "auth":
		return true
	}
	for _, part := range tokens {
		switch part {
		case "pwd", "otp", "totp", "hotp", "pin", "csrf", "secret", "secrets", "password", "passwd":
			return true
		}
	}
	return false
}

// splitCredentialTokens 在转小写前先按分隔符与大小写边界切分词元，保留驼峰单词边界。
func splitCredentialTokens(key string) []string {
	tokens := make([]string, 0, 4)
	for segment := range strings.FieldsFuncSeq(key, isCredentialDelimiter) {
		runes := []rune(segment)
		start := 0
		for index := 1; index < len(runes); index++ {
			prev, curr := runes[index-1], runes[index]
			lowerOrDigitBeforeUpper := unicode.IsUpper(curr) && (unicode.IsLower(prev) || unicode.IsDigit(prev))
			acronymBeforeWord := unicode.IsUpper(curr) && unicode.IsUpper(prev) && index+1 < len(runes) && unicode.IsLower(runes[index+1])
			if lowerOrDigitBeforeUpper || acronymBeforeWord {
				tokens = append(tokens, strings.ToLower(string(runes[start:index])))
				start = index
			}
		}
		tokens = append(tokens, strings.ToLower(string(runes[start:])))
	}
	return tokens
}

func isCredentialDelimiter(r rune) bool {
	return r == '-' || r == '_' || r == '.' || r == ':' || r == '/' || unicode.IsSpace(r)
}
