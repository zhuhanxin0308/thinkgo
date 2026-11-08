package session

import (
	"fmt"
	"strings"

	frameworklog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

const sessionLogRedacted = "[REDACTED]"

// errorLoggerSnapshot 在锁内取得日志器和当前 ID，锁外探测可选级别过滤能力。
func (s *Session) errorLoggerSnapshot() (Logger, string) {
	s.mu.RLock()
	logger, sessionID := s.logger, s.id
	s.mu.RUnlock()
	if filter, ok := logger.(interface{ IsLevelEnabled(string) bool }); ok && !filter.IsLevelEnabled(frameworklog.LevelError) {
		return nil, sessionID
	}
	return logger, sessionID
}

// reportError 仅为会接收错误日志的记录器构建上下文，始终保留原始错误身份。
func (s *Session) reportError(message string, err error, fields map[string]interface{}) error {
	if err == nil {
		return nil
	}
	logger, sessionID := s.errorLoggerSnapshot()
	if logger != nil {
		writeSessionError(logger, sessionID, message, err, fields)
	}
	return err
}

// reportIDError 把存储路径的 Session ID 元数据构造推迟到级别过滤之后。
func (s *Session) reportIDError(message string, err error, reportedID string) error {
	if err == nil {
		return nil
	}
	logger, sessionID := s.errorLoggerSnapshot()
	if logger != nil {
		writeSessionError(logger, sessionID, message, err, map[string]interface{}{"session_id": reportedID})
	}
	return err
}

// writeSessionError 在自定义记录器边界前统一脱敏，不修改调用方传入的字段。
func writeSessionError(logger Logger, sessionID, message string, err error, fields map[string]interface{}) {
	logContext := map[string]interface{}{"component": "session"}
	detail := fmt.Sprintf("%s: %v", message, err)
	if sessionID != "" {
		detail = strings.ReplaceAll(detail, sessionID, sessionLogRedacted)
	}
	for key, value := range fields {
		if strings.Contains(strings.ToLower(key), "session") {
			logContext[key] = sessionLogRedacted
			if id, ok := value.(string); ok && id != "" {
				detail = strings.ReplaceAll(detail, id, sessionLogRedacted)
			}
			continue
		}
		logContext[key] = value
	}
	logger.ErrorCtx(frameworklog.SanitizeErrorTextFully(detail), logContext)
}
