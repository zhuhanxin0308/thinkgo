package exception

import "encoding/json"

// ValidateException 验证异常
// 对应 ThinkPHP 8 的 think\exception\ValidateException
// 用于表单验证失败时抛出，默认返回 422 状态码
type ValidateException struct {
	Field   string            // 验证失败的字段名
	Message string            // 错误消息
	Errors  map[string]string // 批量验证时保存全部字段错误
}

// Error 实现 error 接口
func (e *ValidateException) Error() string {
	if e == nil {
		return "验证异常为空"
	}
	return e.Message
}

// GetError 返回 ThinkPHP ValidateException::getError 对应的错误值。
// 单条验证返回字符串，批量验证返回隔离副本。
func (e *ValidateException) GetError() interface{} {
	if e == nil {
		return nil
	}
	if len(e.Errors) == 0 {
		return e.Message
	}
	result := make(map[string]string, len(e.Errors))
	for field, message := range e.Errors {
		result[field] = message
	}
	return result
}

// GetKey 返回首个验证失败字段，对应 ThinkPHP ValidateException::getKey。
func (e *ValidateException) GetKey() string {
	if e == nil {
		return ""
	}
	return e.Field
}

// NewValidateException 创建验证异常
func NewValidateException(message string, field ...string) *ValidateException {
	e := &ValidateException{Message: message}
	if len(field) > 0 {
		e.Field = field[0]
	}
	return e
}

// NewBatchValidateException 创建包含全部字段错误的批量验证异常。
func NewBatchValidateException(errorsFound map[string]string) *ValidateException {
	errorsCopy := make(map[string]string, len(errorsFound))
	for field, message := range errorsFound {
		errorsCopy[field] = message
	}
	encoded, err := json.Marshal(errorsCopy)
	if err != nil {
		// map[string]string 理论上始终可编码；保留稳定兜底，避免异常构造再次 panic。
		encoded = []byte("{}")
	}
	return &ValidateException{Message: string(encoded), Errors: errorsCopy}
}
