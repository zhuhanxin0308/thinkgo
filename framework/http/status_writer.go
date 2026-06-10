package http

import (
	"bufio"
	"net"
	"net/http"
)

// statusTrackingResponseWriter 包装标准 ResponseWriter，并记录最终写出的状态码。
type statusTrackingResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

// newStatusTrackingResponseWriter 创建带状态跟踪能力的响应写入器。
func newStatusTrackingResponseWriter(w http.ResponseWriter) *statusTrackingResponseWriter {
	return &statusTrackingResponseWriter{
		ResponseWriter: w,
		status:         http.StatusOK,
	}
}

// WriteHeader 记录并透传状态码。
func (w *statusTrackingResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.status = statusCode
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
}

// Write 在隐式写头时同步记录 200 状态。
func (w *statusTrackingResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// Status 返回当前已确认的最终状态码。
func (w *statusTrackingResponseWriter) Status() int {
	return w.status
}

// Flush 透传流式刷新能力。
func (w *statusTrackingResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack 透传底层连接劫持能力。
func (w *statusTrackingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

// Push 透传 HTTP/2 Server Push 能力。
func (w *statusTrackingResponseWriter) Push(target string, opts *http.PushOptions) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}
