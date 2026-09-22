package context

import (
	"errors"
	"net/http"

	frameworksession "github.com/zhuhanxin0308/thinkgo/framework/session"
)

// SetSession 绑定当前请求 Session，使响应发送前完成持久化和 Cookie 提交。
func (r *Response) SetSession(current *frameworksession.Session) *Response {
	if r == nil {
		return nil
	}
	r.session = current
	return r
}

// GetCookie 返回当前响应中待发送 Cookie 的防御性快照。
func (r *Response) GetCookie() []*http.Cookie {
	if r == nil {
		return nil
	}
	var values []string
	if r.header != nil {
		values = r.header.values.Values("Set-Cookie")
	}
	result := make([]*http.Cookie, 0, len(values))
	for _, value := range values {
		parsed, err := http.ParseSetCookie(value)
		if err != nil {
			continue
		}
		copyCookie := *parsed
		result = append(result, &copyCookie)
	}
	return result
}

func (r *Response) saveSession(writer http.ResponseWriter) error {
	if r == nil || r.session == nil {
		return nil
	}
	if err := r.session.SetResponseWriter(writer); err != nil {
		return errors.Join(frameworksession.ErrSessionCookie, err)
	}
	return r.session.Save()
}
