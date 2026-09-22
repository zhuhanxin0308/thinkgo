package context

import "net/http"

// WithResponseWriter 为需要直接复用 net/http.Handler 的请求绑定当前响应写入器。
func WithResponseWriter(writer http.ResponseWriter) RequestOption {
	return func(request *Request) error {
		if isNilHTTPResponseWriter(writer) {
			return ErrInvalidResponseWriter
		}
		request.responseWriter = writer
		return nil
	}
}

// ResponseWriter 返回 HTTP 内核绑定的当前响应写入器；独立构造的 Request 可以没有该能力。
func (r *Request) ResponseWriter() (http.ResponseWriter, bool) {
	if r == nil || isNilHTTPResponseWriter(r.responseWriter) {
		return nil, false
	}
	return r.responseWriter, true
}
