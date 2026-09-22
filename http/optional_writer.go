package http

import (
	"bufio"
	"net"
	"net/http"
)

// adaptiveResponseWriter 隐藏具体包装器的固定方法集，只按原始 writer 的真实能力构造 facade。
type adaptiveResponseWriter struct {
	http.ResponseWriter
	controllerTarget http.ResponseWriter
}

func (w *adaptiveResponseWriter) Unwrap() http.ResponseWriter {
	return unwrapAdaptiveTarget(w.controllerTarget, w.ResponseWriter)
}
func (w *adaptiveResponseWriter) BeforeCommit(hook func(http.Header) error) error {
	return beforeCommitThrough(w.ResponseWriter, hook)
}
func (w *adaptiveResponseWriter) Status() int        { return statusThrough(w.ResponseWriter) }
func (w *adaptiveResponseWriter) CommitError() error { return commitErrorThrough(w.ResponseWriter) }
func (w *adaptiveResponseWriter) ResetUncommitted() bool {
	return resetUncommittedThrough(w.ResponseWriter)
}

type adaptiveFlusher adaptiveResponseWriter

func (w *adaptiveFlusher) Unwrap() http.ResponseWriter {
	return unwrapAdaptiveTarget(w.controllerTarget, w.ResponseWriter)
}
func (w *adaptiveFlusher) BeforeCommit(hook func(http.Header) error) error {
	return beforeCommitThrough(w.ResponseWriter, hook)
}
func (w *adaptiveFlusher) Status() int            { return statusThrough(w.ResponseWriter) }
func (w *adaptiveFlusher) CommitError() error     { return commitErrorThrough(w.ResponseWriter) }
func (w *adaptiveFlusher) ResetUncommitted() bool { return resetUncommittedThrough(w.ResponseWriter) }
func (w *adaptiveFlusher) Flush()                 { _ = w.FlushError() }
func (w *adaptiveFlusher) FlushError() error      { return flushAdaptedResponse(w.ResponseWriter) }

type adaptiveHijacker adaptiveResponseWriter

func (w *adaptiveHijacker) Unwrap() http.ResponseWriter {
	return unwrapAdaptiveTarget(w.controllerTarget, w.ResponseWriter)
}
func (w *adaptiveHijacker) BeforeCommit(hook func(http.Header) error) error {
	return beforeCommitThrough(w.ResponseWriter, hook)
}
func (w *adaptiveHijacker) Status() int            { return statusThrough(w.ResponseWriter) }
func (w *adaptiveHijacker) CommitError() error     { return commitErrorThrough(w.ResponseWriter) }
func (w *adaptiveHijacker) ResetUncommitted() bool { return resetUncommittedThrough(w.ResponseWriter) }
func (w *adaptiveHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return hijackAdaptedResponse(w.ResponseWriter)
}

type adaptivePusher adaptiveResponseWriter

func (w *adaptivePusher) Unwrap() http.ResponseWriter {
	return unwrapAdaptiveTarget(w.controllerTarget, w.ResponseWriter)
}
func (w *adaptivePusher) BeforeCommit(hook func(http.Header) error) error {
	return beforeCommitThrough(w.ResponseWriter, hook)
}
func (w *adaptivePusher) Status() int            { return statusThrough(w.ResponseWriter) }
func (w *adaptivePusher) CommitError() error     { return commitErrorThrough(w.ResponseWriter) }
func (w *adaptivePusher) ResetUncommitted() bool { return resetUncommittedThrough(w.ResponseWriter) }
func (w *adaptivePusher) Push(target string, options *http.PushOptions) error {
	return pushAdaptedResponse(w.ResponseWriter, target, options)
}

type adaptiveFlusherHijacker adaptiveResponseWriter

func (w *adaptiveFlusherHijacker) Unwrap() http.ResponseWriter {
	return unwrapAdaptiveTarget(w.controllerTarget, w.ResponseWriter)
}
func (w *adaptiveFlusherHijacker) BeforeCommit(hook func(http.Header) error) error {
	return beforeCommitThrough(w.ResponseWriter, hook)
}
func (w *adaptiveFlusherHijacker) Status() int        { return statusThrough(w.ResponseWriter) }
func (w *adaptiveFlusherHijacker) CommitError() error { return commitErrorThrough(w.ResponseWriter) }
func (w *adaptiveFlusherHijacker) ResetUncommitted() bool {
	return resetUncommittedThrough(w.ResponseWriter)
}
func (w *adaptiveFlusherHijacker) Flush()            { _ = w.FlushError() }
func (w *adaptiveFlusherHijacker) FlushError() error { return flushAdaptedResponse(w.ResponseWriter) }
func (w *adaptiveFlusherHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return hijackAdaptedResponse(w.ResponseWriter)
}

type adaptiveFlusherPusher adaptiveResponseWriter

func (w *adaptiveFlusherPusher) Unwrap() http.ResponseWriter {
	return unwrapAdaptiveTarget(w.controllerTarget, w.ResponseWriter)
}
func (w *adaptiveFlusherPusher) BeforeCommit(hook func(http.Header) error) error {
	return beforeCommitThrough(w.ResponseWriter, hook)
}
func (w *adaptiveFlusherPusher) Status() int        { return statusThrough(w.ResponseWriter) }
func (w *adaptiveFlusherPusher) CommitError() error { return commitErrorThrough(w.ResponseWriter) }
func (w *adaptiveFlusherPusher) ResetUncommitted() bool {
	return resetUncommittedThrough(w.ResponseWriter)
}
func (w *adaptiveFlusherPusher) Flush()            { _ = w.FlushError() }
func (w *adaptiveFlusherPusher) FlushError() error { return flushAdaptedResponse(w.ResponseWriter) }
func (w *adaptiveFlusherPusher) Push(target string, options *http.PushOptions) error {
	return pushAdaptedResponse(w.ResponseWriter, target, options)
}

type adaptiveHijackerPusher adaptiveResponseWriter

func (w *adaptiveHijackerPusher) Unwrap() http.ResponseWriter {
	return unwrapAdaptiveTarget(w.controllerTarget, w.ResponseWriter)
}
func (w *adaptiveHijackerPusher) BeforeCommit(hook func(http.Header) error) error {
	return beforeCommitThrough(w.ResponseWriter, hook)
}
func (w *adaptiveHijackerPusher) Status() int        { return statusThrough(w.ResponseWriter) }
func (w *adaptiveHijackerPusher) CommitError() error { return commitErrorThrough(w.ResponseWriter) }
func (w *adaptiveHijackerPusher) ResetUncommitted() bool {
	return resetUncommittedThrough(w.ResponseWriter)
}
func (w *adaptiveHijackerPusher) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return hijackAdaptedResponse(w.ResponseWriter)
}
func (w *adaptiveHijackerPusher) Push(target string, options *http.PushOptions) error {
	return pushAdaptedResponse(w.ResponseWriter, target, options)
}

type adaptiveFlusherHijackerPusher adaptiveResponseWriter

func (w *adaptiveFlusherHijackerPusher) Unwrap() http.ResponseWriter {
	return unwrapAdaptiveTarget(w.controllerTarget, w.ResponseWriter)
}
func (w *adaptiveFlusherHijackerPusher) BeforeCommit(hook func(http.Header) error) error {
	return beforeCommitThrough(w.ResponseWriter, hook)
}
func (w *adaptiveFlusherHijackerPusher) Status() int { return statusThrough(w.ResponseWriter) }
func (w *adaptiveFlusherHijackerPusher) CommitError() error {
	return commitErrorThrough(w.ResponseWriter)
}
func (w *adaptiveFlusherHijackerPusher) ResetUncommitted() bool {
	return resetUncommittedThrough(w.ResponseWriter)
}
func (w *adaptiveFlusherHijackerPusher) Flush() { _ = w.FlushError() }
func (w *adaptiveFlusherHijackerPusher) FlushError() error {
	return flushAdaptedResponse(w.ResponseWriter)
}
func (w *adaptiveFlusherHijackerPusher) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return hijackAdaptedResponse(w.ResponseWriter)
}
func (w *adaptiveFlusherHijackerPusher) Push(target string, options *http.PushOptions) error {
	return pushAdaptedResponse(w.ResponseWriter, target, options)
}

// adaptiveWriterStorage 的八种 facade 共用同一块内存，避免热路径闭包和额外堆分配。
type adaptiveWriterStorage struct {
	writer adaptiveResponseWriter
}

func (s *adaptiveWriterStorage) adapt(writer http.ResponseWriter, capabilitySource http.ResponseWriter) http.ResponseWriter {
	s.writer = adaptiveResponseWriter{ResponseWriter: writer, controllerTarget: capabilitySource}
	_, canFlush := capabilitySource.(http.Flusher)
	_, canHijack := capabilitySource.(http.Hijacker)
	_, canPush := capabilitySource.(http.Pusher)
	switch {
	case canFlush && canHijack && canPush:
		return (*adaptiveFlusherHijackerPusher)(&s.writer)
	case canFlush && canHijack:
		return (*adaptiveFlusherHijacker)(&s.writer)
	case canFlush && canPush:
		return (*adaptiveFlusherPusher)(&s.writer)
	case canHijack && canPush:
		return (*adaptiveHijackerPusher)(&s.writer)
	case canFlush:
		return (*adaptiveFlusher)(&s.writer)
	case canHijack:
		return (*adaptiveHijacker)(&s.writer)
	case canPush:
		return (*adaptivePusher)(&s.writer)
	default:
		return &s.writer
	}
}

func unwrapAdaptiveTarget(target, fallback http.ResponseWriter) http.ResponseWriter {
	if target != nil {
		return target
	}
	return fallback
}

// adaptResponseWriterCapabilities 为独立使用场景创建 facade；HTTP 热路径使用请求内嵌 storage。
func adaptResponseWriterCapabilities(writer http.ResponseWriter, capabilitySource http.ResponseWriter) http.ResponseWriter {
	storage := &adaptiveWriterStorage{}
	return storage.adapt(writer, capabilitySource)
}

func beforeCommitThrough(writer http.ResponseWriter, hook func(http.Header) error) error {
	for current := writer; current != nil; {
		if registrar, ok := current.(interface {
			BeforeCommit(func(http.Header) error) error
		}); ok {
			return registrar.BeforeCommit(hook)
		}
		unwrapper, ok := current.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		next := unwrapper.Unwrap()
		if next == current {
			break
		}
		current = next
	}
	return http.ErrNotSupported
}

func statusThrough(writer http.ResponseWriter) int {
	for current := writer; current != nil; {
		if provider, ok := current.(interface{ Status() int }); ok {
			return provider.Status()
		}
		unwrapper, ok := current.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		next := unwrapper.Unwrap()
		if next == current {
			break
		}
		current = next
	}
	return http.StatusOK
}

func commitErrorThrough(writer http.ResponseWriter) error {
	for current := writer; current != nil; {
		if provider, ok := current.(interface{ CommitError() error }); ok {
			return provider.CommitError()
		}
		unwrapper, ok := current.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		next := unwrapper.Unwrap()
		if next == current {
			break
		}
		current = next
	}
	return nil
}

func resetUncommittedThrough(writer http.ResponseWriter) bool {
	for current := writer; current != nil; {
		if resetter, ok := current.(interface{ ResetUncommitted() bool }); ok {
			return resetter.ResetUncommitted()
		}
		unwrapper, ok := current.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		next := unwrapper.Unwrap()
		if next == current {
			break
		}
		current = next
	}
	return false
}

func flushAdaptedResponse(writer http.ResponseWriter) error {
	if provider, ok := writer.(interface{ flushResponse() error }); ok {
		return provider.flushResponse()
	}
	return http.NewResponseController(writer).Flush()
}

func hijackAdaptedResponse(writer http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	if provider, ok := writer.(interface {
		hijackResponse() (net.Conn, *bufio.ReadWriter, error)
	}); ok {
		return provider.hijackResponse()
	}
	return http.NewResponseController(writer).Hijack()
}

func pushAdaptedResponse(writer http.ResponseWriter, target string, options *http.PushOptions) error {
	if provider, ok := writer.(interface {
		pushResponse(string, *http.PushOptions) error
	}); ok {
		return provider.pushResponse(target, options)
	}
	return pushThroughResponseWriter(writer, target, options)
}

func pushThroughResponseWriter(writer http.ResponseWriter, target string, options *http.PushOptions) error {
	for writer != nil {
		if pusher, ok := writer.(http.Pusher); ok {
			return pusher.Push(target, options)
		}
		unwrapper, ok := writer.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		next := unwrapper.Unwrap()
		if next == writer {
			break
		}
		writer = next
	}
	return http.ErrNotSupported
}
