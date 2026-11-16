// Package redact 包含框架不同输出入口共用的脱敏规则。
package redact

import "strings"

var compactCredentialKey = strings.NewReplacer("-", "", "_", "", ".", "")

// CredentialAlias 匹配常见凭据别名，短别名按字段边界判断。
func CredentialAlias(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	compact := compactCredentialKey.Replace(key)
	switch compact {
	case "key", "xkey", "xauth", "pass", "xpass":
		return true
	}
	if strings.Contains(compact, "passphrase") || strings.Contains(compact, "dbpass") ||
		strings.Contains(compact, "masterkey") || strings.Contains(compact, "credential") {
		return true
	}
	// pass 和 auth 也用于指标和状态名称，仅按完整的凭据末段匹配。
	switch key[strings.LastIndexAny(key, "-_.")+1:] {
	case "pass", "auth":
		return true
	}
	for part := range strings.FieldsFuncSeq(key, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
		switch part {
		case "pwd", "otp", "totp", "hotp", "pin", "csrf":
			return true
		}
	}
	return false
}
