package middleware

import (
	"errors"
	"net/http"
	"sync"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/session"
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
	varSessionID := s.Manager.GetConfig().VarSessionID
	requestedID := ""
	if varSessionID != "" {
		requestedID = req.Post(varSessionID)
	}
	requestSession, err := s.Manager.NewRequestSessionWithIDAndSecure(req.Raw(), nil, requestedID, req.IsSsl())
	if err != nil {
		status := http.StatusInternalServerError
		message := "会话初始化失败"
		if errors.Is(err, session.ErrSessionCookie) {
			status = http.StatusBadRequest
			message = "会话 Cookie 非法"
		}
		return sessionErrorResponse(status, message)
	}
	req.WithSession(requestSession)
	commit := &sessionCommitCoordinator{requestSession: requestSession}
	if writer, exists := req.ResponseWriter(); exists {
		if registrar, ok := writer.(interface {
			BeforeCommit(func(http.Header) error) error
		}); ok {
			if err = registrar.BeforeCommit(commit.beforeCommit); err != nil {
				return sessionErrorResponse(http.StatusInternalServerError, "会话提交钩子注册失败")
			}
			commit.registered = true
		}
	}
	response := next(req)
	if response == nil {
		// HTTP 内核稍后生成统一错误响应时仍会触发已注册的提交钩子；独立调用保持 nil 契约。
		return nil
	}
	if commit.registered && (commit.executedBeforeCommit() || response.Committed()) {
		// 标准 Handler 的最终提交已经在钩子内完成；仅发送 1xx 或空响应时由内核最终 CommitEmpty 触发。
		return response
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

// sessionCommitCoordinator 让标准 Handler 的 Session 保存发生在首个最终响应边界之前。
type sessionCommitCoordinator struct {
	lock           sync.Mutex
	requestSession *session.Session
	registered     bool
	executed       bool
}

func (c *sessionCommitCoordinator) beforeCommit(header http.Header) error {
	c.lock.Lock()
	c.executed = true
	c.lock.Unlock()

	writer := &sessionHeaderWriter{header: header}
	if err := c.requestSession.SetResponseWriter(writer); err != nil {
		return newSessionCommitError(errors.Join(err, c.requestSession.CommitResponse()))
	}
	if err := c.requestSession.CommitResponse(); err != nil {
		return newSessionCommitError(err)
	}
	keepLatestSessionCookie(header, c.requestSession.GetConfig().Name)
	return nil
}

func (c *sessionCommitCoordinator) executedBeforeCommit() bool {
	if c == nil {
		return false
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.executed
}

type sessionCommitError struct {
	err    error
	status int
}

func newSessionCommitError(err error) error {
	status := http.StatusInternalServerError
	if errors.Is(err, session.ErrSessionRevoked) || errors.Is(err, session.ErrSessionBusy) {
		status = http.StatusConflict
	}
	return &sessionCommitError{err: err, status: status}
}

func (e *sessionCommitError) Error() string {
	if e == nil || e.err == nil {
		return "会话提交失败"
	}
	return e.err.Error()
}

func (e *sessionCommitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *sessionCommitError) ResponseCommitStatus() int {
	if e == nil {
		return http.StatusInternalServerError
	}
	return e.status
}

// sessionHeaderWriter 只允许 Session 向即将提交的真实 Header 写入 Cookie。
type sessionHeaderWriter struct {
	header http.Header
}

func (w *sessionHeaderWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *sessionHeaderWriter) Write(body []byte) (int, error) { return len(body), nil }
func (w *sessionHeaderWriter) WriteHeader(int)                {}

// keepLatestSessionCookie 合并提交前多次 Save 产生的同名 Cookie，只保留最终状态。
func keepLatestSessionCookie(header http.Header, name string) {
	values := header.Values("Set-Cookie")
	if len(values) < 2 || name == "" {
		return
	}
	latest := -1
	for index, value := range values {
		parsed, err := http.ParseSetCookie(value)
		if err == nil && parsed.Name == name {
			latest = index
		}
	}
	if latest < 0 {
		return
	}
	filtered := make([]string, 0, len(values))
	for index, value := range values {
		parsed, err := http.ParseSetCookie(value)
		if err == nil && parsed.Name == name && index != latest {
			continue
		}
		filtered = append(filtered, value)
	}
	header.Del("Set-Cookie")
	for _, value := range filtered {
		header.Add("Set-Cookie", value)
	}
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
