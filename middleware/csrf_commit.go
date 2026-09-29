package middleware

import (
	"net/http"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
)

// prepareCSRFCookie 返回独立调用中间件时需要附加到内存响应的 Cookie。
// 有真实 writer 时优先注册最终提交钩子；无钩子的标准 writer 则必须在 next
// 之前写头。不能在已提交的 Response 上补 Cookie 并假定客户端已经收到。
func prepareCSRFCookie(req *context.Request, config CSRFConfig, token string, now time.Time) (string, error) {
	pending := context.NewResponse()
	if err := setCSRFCookie(pending, req, config, token, now); err != nil {
		return "", err
	}
	if err := pending.Error(); err != nil {
		return "", err
	}
	cookie := pending.GetHeader("Set-Cookie")
	writer, exists := req.ResponseWriter()
	if !exists {
		return cookie, nil
	}
	return "", writeCSRFCookieBeforeCommit(writer, cookie)
}

// writeCSRFCookieBeforeCommit 只负责真实 Header 的提交时序，可独立验证 writer 契约。
func writeCSRFCookieBeforeCommit(writer http.ResponseWriter, cookie string) error {
	if registrar, ok := writer.(interface {
		BeforeCommit(func(http.Header) error) error
	}); ok {
		if err := registrar.BeforeCommit(func(header http.Header) error {
			header.Add("Set-Cookie", cookie)
			return nil
		}); err != nil {
			return err
		}
		return nil
	}
	writer.Header().Add("Set-Cookie", cookie)
	return nil
}
