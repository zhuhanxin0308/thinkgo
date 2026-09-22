package http

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
)

var (
	// ErrResponseBoundaryCrossed 表示最终响应已经提交或连接所有权已经转移。
	ErrResponseBoundaryCrossed = errors.New("HTTP 响应边界已经越过")
	// ErrInvalidBeforeCommitHook 表示调用方尝试注册空的提交钩子。
	ErrInvalidBeforeCommitHook = errors.New("HTTP 提交钩子不能为空")
)

// beforeCommitHook 在最终响应头提交前执行；1xx 不触发该钩子。
type beforeCommitHook func(http.Header) error

type responseCommitState uint8

const (
	responseStateOpen responseCommitState = iota
	responseStateFinalCommitted
	responseStateHijacked
)

// responseCommitStatusProvider 允许提交钩子把已知冲突映射成安全的最终状态。
type responseCommitStatusProvider interface {
	ResponseCommitStatus() int
}

// isNilHTTPResponseWriter 同时识别 nil 接口和携带 typed nil 的响应写入器。
func isNilHTTPResponseWriter(writer http.ResponseWriter) bool {
	if writer == nil {
		return true
	}
	value := reflect.ValueOf(writer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// statusTrackingResponseWriter 统一管理 Open、Informational、FinalCommitted 和 Hijacked 状态。
// ResponseWriter 与 net/http 一样只允许在单个 Handler 调用链内使用，不额外承诺并发写安全。
type statusTrackingResponseWriter struct {
	http.ResponseWriter
	status       int
	state        responseCommitState
	hooks        []beforeCommitHook
	hooksRun     bool
	commitErr    error
	aborted      bool
	suppressBody bool
}

// newStatusTrackingResponseWriter 创建带最终提交事务和状态跟踪能力的响应写入器。
func newStatusTrackingResponseWriter(w http.ResponseWriter) *statusTrackingResponseWriter {
	return &statusTrackingResponseWriter{
		ResponseWriter: w,
		status:         http.StatusOK,
	}
}

// BeforeCommit 注册最终提交钩子。钩子按注册顺序执行，并且整个请求最多执行一次。
func (w *statusTrackingResponseWriter) BeforeCommit(hook func(http.Header) error) error {
	if w == nil || w.state != responseStateOpen || w.hooksRun {
		return ErrResponseBoundaryCrossed
	}
	if hook == nil {
		return ErrInvalidBeforeCommitHook
	}
	w.hooks = append(w.hooks, hook)
	return nil
}

// WriteHeader 立即透传任意数量的 1xx；首个 2xx-9xx 才锁定最终状态。
func (w *statusTrackingResponseWriter) WriteHeader(statusCode int) {
	if w == nil || w.state != responseStateOpen {
		return
	}
	if isInterimHTTPStatus(statusCode) {
		w.ResponseWriter.WriteHeader(statusCode)
		return
	}
	w.commitFinal(statusCode)
}

// isInterimHTTPStatus 与 net/http 保持一致：除 101 外的 1xx 可以在最终响应前重复发送。
func isInterimHTTPStatus(statusCode int) bool {
	return statusCode >= http.StatusContinue && statusCode < http.StatusOK && statusCode != http.StatusSwitchingProtocols
}

// Write 在隐式最终提交 200 后写出实体；提交钩子失败时拒绝泄漏业务实体。
func (w *statusTrackingResponseWriter) Write(body []byte) (int, error) {
	if w == nil {
		return 0, ErrResponseBoundaryCrossed
	}
	if w.state == responseStateHijacked {
		return 0, http.ErrHijacked
	}
	if w.state == responseStateOpen {
		w.commitFinal(http.StatusOK)
	}
	if w.aborted {
		return 0, w.commitErr
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusTrackingResponseWriter) commitFinal(statusCode int) {
	if w == nil || w.state != responseStateOpen {
		return
	}
	if err := w.runBeforeCommitHooks(); err != nil {
		w.commitFailure(err)
		return
	}
	w.status = statusCode
	w.state = responseStateFinalCommitted
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *statusTrackingResponseWriter) runBeforeCommitHooks() error {
	if w.hooksRun {
		return w.commitErr
	}
	w.hooksRun = true
	var result error
	for _, hook := range w.hooks {
		result = errors.Join(result, invokeBeforeCommitHook(hook, w.Header()))
	}
	w.hooks = nil
	w.commitErr = result
	return result
}

func invokeBeforeCommitHook(hook beforeCommitHook, header http.Header) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("HTTP 提交钩子 panic: %v", recovered)
		}
	}()
	return hook(header)
}

func (w *statusTrackingResponseWriter) commitFailure(err error) {
	status := http.StatusInternalServerError
	var provider responseCommitStatusProvider
	if errors.As(err, &provider) {
		candidate := provider.ResponseCommitStatus()
		if candidate >= http.StatusBadRequest && candidate <= 599 {
			status = candidate
		}
	}
	header := w.Header()
	for key := range header {
		header.Del(key)
	}
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "no-store")
	w.status = status
	w.state = responseStateFinalCommitted
	w.commitErr = err
	w.aborted = true
	w.ResponseWriter.WriteHeader(status)
	if !w.suppressBody {
		_, _ = w.ResponseWriter.Write([]byte(http.StatusText(status)))
	}
}

// CommitEmpty 确保没有显式输出的 Handler 仍经过提交钩子并形成 200 空响应。
func (w *statusTrackingResponseWriter) CommitEmpty() {
	if w != nil && w.state == responseStateOpen {
		w.commitFinal(http.StatusOK)
	}
}

// ResetUncommitted 清除尚未最终提交的业务头，但保留提交钩子供异常响应复用。
func (w *statusTrackingResponseWriter) ResetUncommitted() bool {
	if w == nil || w.state != responseStateOpen || w.hooksRun {
		return false
	}
	for key := range w.Header() {
		w.Header().Del(key)
	}
	w.status = http.StatusOK
	w.commitErr = nil
	w.aborted = false
	return true
}

// SuppressBody 保证 HEAD 的内核级错误也不会输出实体。
func (w *statusTrackingResponseWriter) SuppressBody(suppress bool) {
	if w != nil && w.state == responseStateOpen {
		w.suppressBody = suppress
	}
}

// Status 返回已确认的最终状态；尚未最终提交时返回隐式默认值 200。
func (w *statusTrackingResponseWriter) Status() int {
	if w == nil {
		return http.StatusInternalServerError
	}
	return w.status
}

// Written 判断最终响应是否已经提交或连接是否已经被成功劫持。
func (w *statusTrackingResponseWriter) Written() bool {
	return w != nil && w.state != responseStateOpen
}

// Hijacked 判断底层连接所有权是否已经转移。
func (w *statusTrackingResponseWriter) Hijacked() bool {
	return w != nil && w.state == responseStateHijacked
}

// CommitError 返回提交钩子的累计错误。
func (w *statusTrackingResponseWriter) CommitError() error {
	if w == nil {
		return ErrResponseBoundaryCrossed
	}
	return w.commitErr
}

// Unwrap 允许 http.ResponseController 继续查找底层能力。
func (w *statusTrackingResponseWriter) Unwrap() http.ResponseWriter {
	if w == nil {
		return nil
	}
	return w.ResponseWriter
}

// Flush 是旧具体类型的兼容入口；真实请求链通过自适应 facade 仅在底层支持时暴露 Flusher。
// Deprecated: 新代码应对 ServeHTTP 提供的 writer 使用 http.NewResponseController(writer).Flush()。
func (w *statusTrackingResponseWriter) Flush() {
	_ = w.flushResponse()
}

func (w *statusTrackingResponseWriter) flushResponse() error {
	if w == nil {
		return ErrResponseBoundaryCrossed
	}
	if w.state == responseStateHijacked {
		return http.ErrHijacked
	}
	if w.state == responseStateOpen {
		w.commitFinal(http.StatusOK)
	}
	flusher, ok := w.ResponseWriter.(http.Flusher)
	if !ok {
		return http.ErrNotSupported
	}
	flusher.Flush()
	return w.commitErr
}

// Hijack 是旧具体类型的兼容入口；不支持时稳定返回 http.ErrNotSupported。
func (w *statusTrackingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijackResponse()
}

func (w *statusTrackingResponseWriter) hijackResponse() (net.Conn, *bufio.ReadWriter, error) {
	if w == nil {
		return nil, nil, ErrResponseBoundaryCrossed
	}
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	if w.state != responseStateOpen {
		return nil, nil, ErrResponseBoundaryCrossed
	}
	if err := w.runBeforeCommitHooks(); err != nil {
		w.commitFailure(err)
		return nil, nil, err
	}
	connection, readWriter, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	w.state = responseStateHijacked
	return connection, readWriter, nil
}

// Push 是旧具体类型的兼容入口；不支持时稳定返回 http.ErrNotSupported。
func (w *statusTrackingResponseWriter) Push(target string, opts *http.PushOptions) error {
	return w.pushResponse(target, opts)
}

func (w *statusTrackingResponseWriter) pushResponse(target string, opts *http.PushOptions) error {
	if w == nil || w.state == responseStateHijacked {
		return ErrResponseBoundaryCrossed
	}
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

// headResponseWriter 保留状态码和响应头，但丢弃 HEAD 动态响应的实体字节。
type headResponseWriter struct {
	http.ResponseWriter
}

// Status 返回底层已记录状态，供标准库路由处理器生成终结器响应快照。
func (w *headResponseWriter) Status() int {
	if provider, ok := w.ResponseWriter.(interface{ Status() int }); ok {
		return provider.Status()
	}
	return http.StatusOK
}

func (w *headResponseWriter) Write(body []byte) (int, error) {
	w.ResponseWriter.WriteHeader(http.StatusOK)
	if provider, ok := w.ResponseWriter.(interface{ CommitError() error }); ok {
		if err := provider.CommitError(); err != nil {
			return 0, err
		}
	}
	return len(body), nil
}

// Unwrap 允许 ResponseController 穿透 HEAD 实体抑制层。
func (w *headResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *headResponseWriter) Flush() {
	_ = w.flushResponse()
}

func (w *headResponseWriter) flushResponse() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *headResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijackResponse()
}

func (w *headResponseWriter) hijackResponse() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *headResponseWriter) Push(target string, options *http.PushOptions) error {
	return w.pushResponse(target, options)
}

func (w *headResponseWriter) pushResponse(target string, options *http.PushOptions) error {
	return pushThroughResponseWriter(w.ResponseWriter, target, options)
}
