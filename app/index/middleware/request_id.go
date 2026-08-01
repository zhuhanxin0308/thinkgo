package middleware

import (
	"strings"
	"unicode"

	"github.com/google/uuid"

	"thinkgo/framework/context"
)

const RequestIDKey = "request_id"

const (
	maxRequestIDLength = 128
)

// RequestID 为每个请求写入稳定的追踪编号，并透传到响应头。
func RequestID(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
	requestID := strings.TrimSpace(req.Header("X-Request-ID"))
	if !isSafeRequestID(requestID) {
		requestID = uuid.NewString()
	}

	req.Set(RequestIDKey, requestID)

	resp := next(req)
	if resp == nil {
		resp = context.NewResponse()
	}
	resp.Header("X-Request-ID", requestID)
	return resp
}

// isSafeRequestID 限制客户端追踪标识的长度和字符集，避免日志高基数与响应放大。
func isSafeRequestID(value string) bool {
	if value == "" || len(value) > maxRequestIDLength {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}
