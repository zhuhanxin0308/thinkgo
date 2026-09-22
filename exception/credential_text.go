package exception

import frameworkLog "github.com/zhuhanxin0308/thinkgo/v3/log"

// safeExceptionText 完整遮蔽响应中的凭据；错误方法自身发生 panic 时仍保留类型诊断。
func safeExceptionText(value interface{}) string {
	return safeExceptionTextWith(value, sanitizeExceptionText)
}

// safeExceptionLogText 只在日志中保留用户指定的四字符前缀，不把此策略扩散到公开响应。
func safeExceptionLogText(value interface{}) string {
	return safeExceptionTextWith(value, sanitizeExceptionLogText)
}

func sanitizeExceptionLogText(value string) string {
	return normalizeExceptionText(frameworkLog.SanitizeErrorText(value))
}
