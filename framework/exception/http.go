package exception

import "fmt"

// HttpException HTTP 异常
// 对应 ThinkPHP 8 的 think\exception\HttpException
type HttpException struct {
	StatusCode int                    // HTTP 状态码
	Message    string                 // 错误消息
	Data       map[string]interface{} // 附加数据
}

// Error 实现 error 接口
func (e *HttpException) Error() string {
	if e == nil {
		return "HTTP 异常为空"
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// NewHttpException 创建 HTTP 异常
func NewHttpException(code int, message string) *HttpException {
	return &HttpException{
		StatusCode: code,
		Message:    message,
		Data:       make(map[string]interface{}),
	}
}

// WithData 链式设置附加数据
func (e *HttpException) WithData(data map[string]interface{}) *HttpException {
	if e == nil {
		return nil
	}
	e.Data = cloneExceptionData(data)
	return e
}

func cloneExceptionData(data map[string]interface{}) map[string]interface{} {
	if data == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(data))
	for key, value := range data {
		cloned[key] = value
	}
	return cloned
}
