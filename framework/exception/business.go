package exception

import (
	"fmt"
	"net/http"
)

// BusinessException 业务异常。
// 用于表达业务流程内的预期失败，而不是系统级故障。
type BusinessException struct {
	Code       int
	Message    string
	HTTPStatus int
	Data       map[string]interface{}
	Cause      error
}

// Error 实现 error 接口。
func (e *BusinessException) Error() string {
	if e == nil {
		return "业务异常为空"
	}
	return fmt.Sprintf("业务异常 [%d]: %s", e.Code, e.Message)
}

// StatusCode 返回业务异常对应的 HTTP 状态码。
func (e *BusinessException) StatusCode() int {
	if e == nil {
		return http.StatusInternalServerError
	}
	if validExceptionStatus(e.HTTPStatus) {
		return e.HTTPStatus
	}
	return http.StatusInternalServerError
}

// NewBusinessException 创建业务异常，默认映射为 400 Bad Request。
func NewBusinessException(code int, message string) *BusinessException {
	return &BusinessException{
		Code:       code,
		Message:    message,
		HTTPStatus: http.StatusBadRequest,
		Data:       nil,
		Cause:      nil,
	}
}

// WithStatus 显式指定业务异常的 HTTP 状态码。
func (e *BusinessException) WithStatus(status int) *BusinessException {
	if e == nil {
		return nil
	}
	e.HTTPStatus = status
	return e
}

// WithData 链式设置可安全返回给客户端的结构化业务数据。
func (e *BusinessException) WithData(data map[string]interface{}) *BusinessException {
	if e == nil {
		return nil
	}
	e.Data = cloneExceptionData(data)
	return e
}

// WithCause 链式挂接内部原始错误，仅用于日志和 errors.Is/errors.As 判断，不直接暴露给客户端。
func (e *BusinessException) WithCause(err error) *BusinessException {
	if e == nil {
		return nil
	}
	e.Cause = err
	return e
}

func validExceptionStatus(status int) bool {
	return status >= http.StatusBadRequest && status <= 599
}

// Unwrap 返回底层原始错误，便于上层使用 errors.Is/errors.As 做类型判断。
func (e *BusinessException) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
