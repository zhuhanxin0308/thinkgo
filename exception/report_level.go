package exception

import (
	"net/http"

	frameworkLog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

// exceptionLogLevelEnabled 通过可选能力提前过滤日志，保持旧 Logger 实现的报告行为。
func exceptionLogLevelEnabled(logger Logger, level string) bool {
	if logger == nil {
		return false
	}
	if filter, ok := logger.(interface{ IsLevelEnabled(string) bool }); ok {
		return filter.IsLevelEnabled(level)
	}
	return true
}

// exceptionStatusLogLevel 保持客户端异常为 warning、服务端异常为 error 的既有分类。
func exceptionStatusLogLevel(status int) string {
	if status >= http.StatusInternalServerError {
		return frameworkLog.LevelError
	}
	return frameworkLog.LevelWarning
}
