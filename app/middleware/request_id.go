package middleware

import (
	"strings"

	"github.com/google/uuid"

	"thinkgo/framework"
	"thinkgo/framework/context"
)

const RequestIDKey = "request_id"

func init() {
	// 注册全局请求编号中间件，便于日志、响应头和业务链路使用同一个追踪编号。
	framework.RegisterGlobalMiddleware(RequestID)
}

// RequestID 为每个请求写入稳定的追踪编号，并透传到响应头。
func RequestID(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
	requestID := strings.TrimSpace(req.Header("X-Request-ID"))
	if requestID == "" {
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
