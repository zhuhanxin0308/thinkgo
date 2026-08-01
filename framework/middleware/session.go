package middleware

import (
	"errors"
	"net/http"

	"thinkgo/framework/context"
	"thinkgo/framework/session"
)

// Session 管理每个请求独立的会话初始化、上下文注入和持久化。
type Session struct {
	Manager *session.Session
}

// Handle 在初始化失败时阻止下游执行，并在保存失败时覆盖虚假的业务成功响应。
func (s *Session) Handle(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
	if s == nil || s.Manager == nil || req == nil || req.Raw() == nil || next == nil {
		return sessionErrorResponse(http.StatusInternalServerError, "会话服务不可用")
	}
	requestSession, err := s.Manager.NewRequestSessionWithSecure(req.Raw(), nil, req.IsSsl())
	if err != nil {
		status := http.StatusInternalServerError
		message := "会话初始化失败"
		if errors.Is(err, session.ErrSessionCookie) {
			status = http.StatusBadRequest
			message = "会话 Cookie 非法"
		}
		return sessionErrorResponse(status, message)
	}
	req.Set("_session", requestSession)
	response := next(req)
	if response == nil {
		// 下游 nil 由 HTTP 内核统一处理，不能为写 Cookie 伪造一个成功响应。
		return nil
	}
	adapter := &ResponseAdapter{response: response}
	if err = requestSession.SetResponseWriter(adapter); err != nil {
		return sessionErrorResponse(http.StatusInternalServerError, "会话响应初始化失败")
	}
	if err = requestSession.Save(); err != nil {
		if errors.Is(err, session.ErrSessionRevoked) || errors.Is(err, session.ErrSessionBusy) {
			return sessionErrorResponse(http.StatusConflict, "会话状态已变化，请重试")
		}
		return sessionErrorResponse(http.StatusInternalServerError, "会话保存失败")
	}
	if err = adapter.Commit(); err != nil {
		return sessionErrorResponse(http.StatusInternalServerError, "会话响应头提交失败")
	}
	return response
}

// ResponseAdapter 将内存响应安全适配为仅用于写响应头的 http.ResponseWriter。
type ResponseAdapter struct {
	response *context.Response
	header   http.Header
}

func (r *ResponseAdapter) Header() http.Header {
	if r == nil || r.response == nil {
		return make(http.Header)
	}
	if r.header == nil {
		r.header = make(http.Header)
	}
	return r.header
}

func (r *ResponseAdapter) Write(data []byte) (int, error) {
	return len(data), nil
}

func (r *ResponseAdapter) WriteHeader(int) {}

// Commit 通过 context.Response 的校验入口提交暂存头，避免修改 Headers 返回的防御性副本。
func (r *ResponseAdapter) Commit() error {
	if r == nil || r.response == nil {
		return context.ErrInvalidResponseWriter
	}
	for key, values := range r.header {
		for _, value := range values {
			r.response.AddHeader(key, value)
		}
	}
	if err := r.response.Error(); err != nil {
		return err
	}
	r.header = make(http.Header)
	return nil
}

func sessionErrorResponse(status int, message string) *context.Response {
	return context.NewResponse().Abort(status, map[string]interface{}{"message": message})
}
