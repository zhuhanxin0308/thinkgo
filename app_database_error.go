package framework

import (
	"regexp"
	"strings"
)

var (
	// databaseURISecretPattern 覆盖常见 scheme://user:password@host 形式的连接串。
	databaseURISecretPattern = regexp.MustCompile(`(?i)(://[^/\s:@]+:)[^@\s/]+(@)`)
	// 原生 MySQL DSN 不带 scheme；歧义或多行凭据优先扩大脱敏范围，避免泄密。
	databaseMySQLSecretPattern = regexp.MustCompile(`(?is)(:).*(@(?:tcp[46]?|unix)\()`)
	// databaseKeySecretPattern 覆盖 DSN、JSON 错误和 key=value 形式的敏感字段。
	databaseKeySecretPattern = regexp.MustCompile(`(?i)(\b(?:password|passwd|pwd|secret|token|authorization|api[_-]?key|refresh[_-]?token)\b["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^,\s;)&]+)`)
)

// redactDatabaseConnectionError 脱敏连接器错误文本，避免驱动把凭据带入应用日志。
func redactDatabaseConnectionError(err error) string {
	if err == nil {
		return "未知数据库连接错误"
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "未知数据库连接错误"
	}
	message = databaseMySQLSecretPattern.ReplaceAllString(message, `${1}[REDACTED]$2`)
	message = databaseURISecretPattern.ReplaceAllString(message, `${1}[REDACTED]$2`)
	message = databaseKeySecretPattern.ReplaceAllString(message, `${1}[REDACTED]`)
	message = strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(message)
	return message
}
