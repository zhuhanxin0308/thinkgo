package exception

import "fmt"

// HttpException HTTP 异常
// 对应 ThinkPHP 8 的 think\exception\HttpException
type HttpException struct {
	StatusCode      int                    // HTTP 状态码
	Message         string                 // 错误消息
	Data            map[string]interface{} // 附加数据
	dataSnapshotErr error
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

// WithData 隔离附加数据并保留 Go 类型；无法复制的数据由 Render 返回错误。
func (e *HttpException) WithData(data map[string]interface{}) *HttpException {
	if e == nil {
		return nil
	}
	e.Data, e.dataSnapshotErr = snapshotExceptionData(data, nil)
	return e
}
