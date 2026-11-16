package exception

import frameworkLog "github.com/zhuhanxin0308/thinkgo/v3/log"

// safeExceptionText 完整遮蔽响应中的凭据；错误方法自身发生 panic 时仍保留类型诊断。
func safeExceptionText(value interface{}) string {
	return sanitizeExceptionText(exceptionValueText(value))
}

// safeExceptionLogText 只在日志中保留用户指定的四字符前缀，不把此策略扩散到公开响应。
func safeExceptionLogText(value interface{}) string {
	return sanitizeExceptionLogText(exceptionValueText(value))
}

func sanitizeExceptionLogText(value string) string {
	return normalizeExceptionText(frameworkLog.SanitizeErrorText(value))
}

func (h *Handle) reportLogMessage(level, value string, fields map[string]interface{}) {
	if logger, builtIn := h.Log.(*frameworkLog.Log); builtIn {
		logger.RecordLimitedContext(value, level, fields, maxExceptionLogTextBytes)
		return
	}
	value = sanitizeExceptionLogText(value)
	if level == frameworkLog.LevelError {
		h.Log.ErrorCtx(value, fields)
		return
	}
	h.Log.WarningCtx(value, fields)
}
