package middleware

import (
	"net/http"
	"thinkgo/framework/context"
	"thinkgo/framework/session"
)

// Session 会话中间件
// 负责在请求开始时初始化 Session，请求结束时保存 Session
// 注意：Session 和 Cookie 应为请求级实例，不应使用全局单例
type Session struct {
	Manager *session.Session
}

// Handle 处理请求的 Session 生命周期
// 为每个请求创建独立的 Session 实例（并发安全），不使用全局单例
func (s *Session) Handle(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
	// 创建请求级 Session 实例（从请求 Cookie 中读取 Session ID）
	reqSession := s.Manager.NewRequestSession(req.Raw(), nil)
	req.Set("_session", reqSession)

	// 执行后续中间件和控制器
	resp := next(req)

	// 保存 Session 数据并将 Session Cookie 写入响应
	reqSession.SetResponseWriter(&ResponseAdapter{resp})
	if err := reqSession.Save(); err != nil {
		// Save 内部已经统一记录错误日志，这里保留返回值处理避免静默丢失失败信号。
		_ = err
	}

	return resp
}

// ResponseAdapter 将 context.Response 适配为 http.ResponseWriter
// 用于在响应已创建后设置 Set-Cookie 头
type ResponseAdapter struct {
	resp *context.Response
}

// Header 返回响应头（用于 http.SetCookie 写入 Cookie）
func (r *ResponseAdapter) Header() http.Header {
	return r.resp.Headers()
}

// Write 写入响应体（Cookie 设置不需要此方法）
func (r *ResponseAdapter) Write([]byte) (int, error) {
	return 0, nil
}

// WriteHeader 写入状态码（Cookie 设置不需要此方法）
func (r *ResponseAdapter) WriteHeader(statusCode int) {
	// Cookie 设置不需要此方法
}
