package exception

import "fmt"

// ValidateException 验证异常
// 对应 ThinkPHP 8 的 think\exception\ValidateException
// 用于表单验证失败时抛出，默认返回 422 状态码
type ValidateException struct {
	Field   string // 验证失败的字段名
	Message string // 错误消息
}

// Error 实现 error 接口
func (e *ValidateException) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("验证失败 [%s]: %s", e.Field, e.Message)
	}
	return fmt.Sprintf("验证失败: %s", e.Message)
}

// NewValidateException 创建验证异常
func NewValidateException(message string, field ...string) *ValidateException {
	e := &ValidateException{Message: message}
	if len(field) > 0 {
		e.Field = field[0]
	}
	return e
}
