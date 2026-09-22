package http

import (
	"bufio"
	"errors"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// responseProtocolRecorder 记录全部 WriteHeader 调用，避免 ResponseRecorder 隐藏 1xx 序列。
type responseProtocolRecorder struct {
	header   stdhttp.Header
	statuses []int
	body     []byte
}

func (r *responseProtocolRecorder) Header() stdhttp.Header {
	if r.header == nil {
		r.header = make(stdhttp.Header)
	}
	return r.header
}

func (r *responseProtocolRecorder) WriteHeader(status int) {
	r.statuses = append(r.statuses, status)
}

func (r *responseProtocolRecorder) Write(body []byte) (int, error) {
	r.body = append(r.body, body...)
	return len(body), nil
}

func TestStatusWriterKeepsInformationalResponsesOpen(t *testing.T) {
	recorder := &responseProtocolRecorder{}
	writer := newStatusTrackingResponseWriter(recorder)
	hookCalls := 0
	if err := writer.BeforeCommit(func(header stdhttp.Header) error {
		hookCalls++
		header.Set("X-Before-Commit", "ready")
		return nil
	}); err != nil {
		t.Fatalf("注册提交钩子失败: %v", err)
	}

	writer.WriteHeader(stdhttp.StatusEarlyHints)
	writer.WriteHeader(stdhttp.StatusContinue)
	if writer.Written() || hookCalls != 0 {
		t.Fatalf("1xx 不应关闭最终响应: written=%t hooks=%d", writer.Written(), hookCalls)
	}
	writer.WriteHeader(stdhttp.StatusCreated)
	writer.WriteHeader(stdhttp.StatusNoContent)
	if _, err := writer.Write([]byte("created")); err != nil {
		t.Fatalf("写入最终实体失败: %v", err)
	}

	if !reflect.DeepEqual(recorder.statuses, []int{stdhttp.StatusEarlyHints, stdhttp.StatusContinue, stdhttp.StatusCreated}) {
		t.Fatalf("状态序列错误: %#v", recorder.statuses)
	}
	if writer.Status() != stdhttp.StatusCreated || !writer.Written() || hookCalls != 1 {
		t.Fatalf("最终状态错误: status=%d written=%t hooks=%d", writer.Status(), writer.Written(), hookCalls)
	}
	if recorder.Header().Get("X-Before-Commit") != "ready" || string(recorder.body) != "created" {
		t.Fatalf("提交钩子或实体丢失: header=%v body=%q", recorder.Header(), string(recorder.body))
	}
}

func TestStatusWriterIgnoresInformationalResponseAfterFinalSelection(t *testing.T) {
	recorder := &responseProtocolRecorder{}
	writer := newStatusTrackingResponseWriter(recorder)
	writer.WriteHeader(stdhttp.StatusAccepted)
	writer.WriteHeader(stdhttp.StatusEarlyHints)
	if !reflect.DeepEqual(recorder.statuses, []int{stdhttp.StatusAccepted}) {
		t.Fatalf("最终状态后不得发送 1xx: %#v", recorder.statuses)
	}
}

// TestSwitchingProtocolsIsFinalResponseBoundary 验证 101 虽属于 1xx，
// 但按 net/http 语义是升级连接的最终响应，不能继续发送 Early Hints 或延迟提交钩子。
func TestSwitchingProtocolsIsFinalResponseBoundary(t *testing.T) {
	recorder := &responseProtocolRecorder{}
	writer := newStatusTrackingResponseWriter(recorder)
	hookCalls := 0
	if err := writer.BeforeCommit(func(stdhttp.Header) error {
		hookCalls++
		return nil
	}); err != nil {
		t.Fatalf("注册提交钩子失败: %v", err)
	}

	writer.WriteHeader(stdhttp.StatusSwitchingProtocols)
	writer.WriteHeader(stdhttp.StatusEarlyHints)
	if !writer.Written() || writer.Status() != stdhttp.StatusSwitchingProtocols || hookCalls != 1 {
		t.Fatalf("101 必须形成最终提交边界: written=%t status=%d hooks=%d", writer.Written(), writer.Status(), hookCalls)
	}
	if !reflect.DeepEqual(recorder.statuses, []int{stdhttp.StatusSwitchingProtocols}) {
		t.Fatalf("101 后不得继续透传 1xx: %#v", recorder.statuses)
	}

	compressedRecorder := &responseProtocolRecorder{}
	request := httptest.NewRequest(stdhttp.MethodGet, "http://example.com", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	compressed := NewCompressionResponseWriter(compressedRecorder, request, 1, map[string]int{"gzip": 1})
	compressed.WriteHeader(stdhttp.StatusSwitchingProtocols)
	compressed.WriteHeader(stdhttp.StatusEarlyHints)
	if err := compressed.Close(); err != nil {
		t.Fatalf("关闭 101 压缩包装器失败: %v", err)
	}
	if !reflect.DeepEqual(compressedRecorder.statuses, []int{stdhttp.StatusSwitchingProtocols}) || compressedRecorder.Header().Get("Content-Encoding") != "" {
		t.Fatalf("101 必须直接透传且不得压缩: statuses=%#v headers=%v", compressedRecorder.statuses, compressedRecorder.Header())
	}
}

type testCommitFailure struct {
	status int
}

func (e *testCommitFailure) Error() string { return "提交前失败" }

func (e *testCommitFailure) ResponseCommitStatus() int { return e.status }

func TestStatusWriterReplacesFailedCommitBeforeBoundary(t *testing.T) {
	recorder := &responseProtocolRecorder{}
	writer := newStatusTrackingResponseWriter(recorder)
	if err := writer.BeforeCommit(func(stdhttp.Header) error {
		return &testCommitFailure{status: stdhttp.StatusConflict}
	}); err != nil {
		t.Fatalf("注册失败钩子失败: %v", err)
	}

	written, err := writer.Write([]byte("business-success"))
	if written != 0 || err == nil {
		t.Fatalf("提交失败后不应写出业务实体: written=%d err=%v", written, err)
	}
	if !reflect.DeepEqual(recorder.statuses, []int{stdhttp.StatusConflict}) {
		t.Fatalf("提交失败状态错误: %#v", recorder.statuses)
	}
	if string(recorder.body) != stdhttp.StatusText(stdhttp.StatusConflict) {
		t.Fatalf("提交失败响应不安全: %q", string(recorder.body))
	}
	if writer.Status() != stdhttp.StatusConflict || !writer.Written() {
		t.Fatalf("提交失败状态未锁定: status=%d written=%t", writer.Status(), writer.Written())
	}
	if err := writer.BeforeCommit(func(stdhttp.Header) error { return nil }); !errors.Is(err, ErrResponseBoundaryCrossed) {
		t.Fatalf("最终提交后注册钩子应失败: %v", err)
	}
}

func TestStatusWriterContainsPanickingBeforeCommitHook(t *testing.T) {
	recorder := &responseProtocolRecorder{}
	writer := newStatusTrackingResponseWriter(recorder)
	if err := writer.BeforeCommit(nil); !errors.Is(err, ErrInvalidBeforeCommitHook) {
		t.Fatalf("空提交钩子必须被拒绝: %v", err)
	}
	writer.Header().Set("X-Business", "must-not-leak")
	if err := writer.BeforeCommit(func(stdhttp.Header) error {
		panic("commit hook failure")
	}); err != nil {
		t.Fatalf("注册 panic 钩子失败: %v", err)
	}

	writer.WriteHeader(stdhttp.StatusCreated)
	if writer.Status() != stdhttp.StatusInternalServerError || string(recorder.body) != stdhttp.StatusText(stdhttp.StatusInternalServerError) {
		t.Fatalf("panic 钩子必须安全改写为 500: status=%d body=%q", writer.Status(), string(recorder.body))
	}
	if recorder.Header().Get("X-Business") != "" || !strings.Contains(writer.CommitError().Error(), "panic") {
		t.Fatalf("panic 钩子不得泄漏业务头且必须保留诊断错误: header=%v err=%v", recorder.Header(), writer.CommitError())
	}
	if writer.Unwrap() != recorder {
		t.Fatal("旧具体 writer 的 Unwrap 必须保持 ResponseController 兼容")
	}
}

func TestCompressionForwardsMultipleInformationalResponses(t *testing.T) {
	recorder := &responseProtocolRecorder{}
	request := httptest.NewRequest(stdhttp.MethodGet, "http://example.com", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	writer := NewCompressionResponseWriter(recorder, request, 1, map[string]int{"gzip": 1})

	writer.WriteHeader(stdhttp.StatusEarlyHints)
	writer.WriteHeader(stdhttp.StatusProcessing)
	writer.WriteHeader(stdhttp.StatusOK)
	if _, err := writer.Write([]byte("payload-payload")); err != nil {
		t.Fatalf("写入压缩实体失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭压缩响应失败: %v", err)
	}

	if !reflect.DeepEqual(recorder.statuses, []int{stdhttp.StatusEarlyHints, stdhttp.StatusProcessing, stdhttp.StatusOK}) {
		t.Fatalf("压缩层破坏了 1xx 序列: %#v", recorder.statuses)
	}
}

func TestCompressionIgnoresInformationalResponseAfterFinalSelection(t *testing.T) {
	recorder := &responseProtocolRecorder{}
	request := httptest.NewRequest(stdhttp.MethodGet, "http://example.com", nil)
	writer := NewCompressionResponseWriter(recorder, request, 1, nil)
	writer.WriteHeader(stdhttp.StatusAccepted)
	writer.WriteHeader(stdhttp.StatusEarlyHints)
	if _, err := writer.Write([]byte("accepted")); err != nil {
		t.Fatalf("写入最终实体失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭最终响应失败: %v", err)
	}
	if !reflect.DeepEqual(recorder.statuses, []int{stdhttp.StatusAccepted}) {
		t.Fatalf("压缩层最终状态后不得发送 1xx: %#v", recorder.statuses)
	}
}

type optionalCapabilityWriter struct {
	*httptest.ResponseRecorder
	flushes int
}

func (w *optionalCapabilityWriter) Flush() {
	w.flushes++
}

func TestAdaptiveWriterExposesOnlyRealCapabilities(t *testing.T) {
	plainRecorder := &responseProtocolRecorder{}
	plainStatus := newStatusTrackingResponseWriter(plainRecorder)
	plain := adaptResponseWriterCapabilities(plainStatus, plainRecorder)
	if _, ok := plain.(stdhttp.Flusher); ok {
		t.Fatal("底层不支持 Flusher 时真实请求链不得声称支持")
	}
	if _, ok := plain.(stdhttp.Hijacker); ok {
		t.Fatal("底层不支持 Hijacker 时真实请求链不得声称支持")
	}
	if _, ok := plain.(stdhttp.Pusher); ok {
		t.Fatal("底层不支持 Pusher 时真实请求链不得声称支持")
	}
	if unwrapped, ok := plain.(interface{ Unwrap() stdhttp.ResponseWriter }); !ok || unwrapped.Unwrap() != plainRecorder {
		t.Fatal("自适应 writer 必须让 ResponseController 直接访问真实连接能力")
	}
	if err := stdhttp.NewResponseController(plain).Flush(); !errors.Is(err, stdhttp.ErrNotSupported) {
		t.Fatalf("ResponseController 不得穿透到旧包装器的兼容 Flush: %v", err)
	}

	capableTarget := &optionalCapabilityWriter{ResponseRecorder: httptest.NewRecorder()}
	capableStatus := newStatusTrackingResponseWriter(capableTarget)
	capable := adaptResponseWriterCapabilities(capableStatus, capableTarget)
	flusher, ok := capable.(stdhttp.Flusher)
	if !ok {
		t.Fatal("底层支持 Flusher 时真实请求链应暴露该能力")
	}
	flusher.Flush()
	if capableTarget.flushes != 1 || capableStatus.Status() != stdhttp.StatusOK || !capableStatus.Written() {
		t.Fatalf("Flush 未经过状态边界: flushes=%d status=%d written=%t", capableTarget.flushes, capableStatus.Status(), capableStatus.Written())
	}
}

type hijackCapabilityWriter struct {
	*responseProtocolRecorder
	connection net.Conn
	err        error
}

func (w *hijackCapabilityWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.err != nil {
		return nil, nil, w.err
	}
	return w.connection, bufio.NewReadWriter(bufio.NewReader(w.connection), bufio.NewWriter(w.connection)), nil
}

func TestAdaptiveHijackRunsCommitHooksBeforeTransfer(t *testing.T) {
	wantErr := errors.New("hijack probe")
	target := &hijackCapabilityWriter{responseProtocolRecorder: &responseProtocolRecorder{}, err: wantErr}
	status := newStatusTrackingResponseWriter(target)
	hookCalls := 0
	if err := status.BeforeCommit(func(stdhttp.Header) error {
		hookCalls++
		return nil
	}); err != nil {
		t.Fatalf("注册 Hijack 钩子失败: %v", err)
	}
	writer := adaptResponseWriterCapabilities(status, target)
	hijacker, ok := writer.(stdhttp.Hijacker)
	if !ok {
		t.Fatal("底层支持 Hijacker 时真实请求链应暴露该能力")
	}
	if _, _, err := hijacker.Hijack(); !errors.Is(err, wantErr) {
		t.Fatalf("Hijack 错误未透传: %v", err)
	}
	if hookCalls != 1 || status.Hijacked() {
		t.Fatalf("失败的 Hijack 不应转移所有权: hooks=%d hijacked=%t", hookCalls, status.Hijacked())
	}
}

func TestAdaptiveHijackLocksSuccessfulOwnershipTransfer(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	t.Cleanup(func() {
		_ = serverConnection.Close()
		_ = clientConnection.Close()
	})
	target := &hijackCapabilityWriter{responseProtocolRecorder: &responseProtocolRecorder{}, connection: serverConnection}
	status := newStatusTrackingResponseWriter(target)
	writer := adaptResponseWriterCapabilities(status, target)
	hijacker := writer.(stdhttp.Hijacker)
	connection, _, err := hijacker.Hijack()
	if err != nil || connection != serverConnection {
		t.Fatalf("连接劫持失败: connection=%v err=%v", connection, err)
	}
	if !status.Hijacked() || !status.Written() {
		t.Fatalf("成功 Hijack 必须锁定所有权: hijacked=%t written=%t", status.Hijacked(), status.Written())
	}
	if _, err = writer.Write([]byte("late-http")); !errors.Is(err, stdhttp.ErrHijacked) {
		t.Fatalf("Hijack 后不得继续 HTTP 写入: %v", err)
	}
}

func TestCompressionRejectsHijackAfterBufferedEntity(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	t.Cleanup(func() {
		_ = serverConnection.Close()
		_ = clientConnection.Close()
	})
	target := &hijackCapabilityWriter{responseProtocolRecorder: &responseProtocolRecorder{}, connection: serverConnection}
	request := httptest.NewRequest(stdhttp.MethodGet, "http://example.com", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	writer := NewCompressionResponseWriter(target, request, 1024, map[string]int{"gzip": 1})
	if _, err := writer.Write([]byte("buffered")); err != nil {
		t.Fatalf("准备压缩缓冲失败: %v", err)
	}
	if _, _, err := writer.Hijack(); !errors.Is(err, ErrResponseBoundaryCrossed) {
		t.Fatalf("存在响应缓冲时不得转移连接所有权: %v", err)
	}
}

type pusherCapabilityWriter struct {
	*responseProtocolRecorder
	pushes int
}

func (w *pusherCapabilityWriter) Push(string, *stdhttp.PushOptions) error {
	w.pushes++
	return nil
}

type flusherHijackerCapabilityWriter struct {
	*optionalCapabilityWriter
	err error
}

func (w *flusherHijackerCapabilityWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, w.err
}

type flusherPusherCapabilityWriter struct {
	*optionalCapabilityWriter
	pushes int
}

func (w *flusherPusherCapabilityWriter) Push(string, *stdhttp.PushOptions) error {
	w.pushes++
	return nil
}

type hijackerPusherCapabilityWriter struct {
	*hijackCapabilityWriter
	pushes int
}

func (w *hijackerPusherCapabilityWriter) Push(string, *stdhttp.PushOptions) error {
	w.pushes++
	return nil
}

type allCapabilityWriter struct {
	*flusherHijackerCapabilityWriter
	pushes int
}

func (w *allCapabilityWriter) Push(string, *stdhttp.PushOptions) error {
	w.pushes++
	return nil
}

func TestAdaptiveWriterCoversEveryCapabilityCombination(t *testing.T) {
	hijackErr := errors.New("capability hijack probe")
	tests := []struct {
		name   string
		source stdhttp.ResponseWriter
		flush  bool
		hijack bool
		push   bool
	}{
		{name: "plain", source: &responseProtocolRecorder{}},
		{name: "flush", source: &optionalCapabilityWriter{ResponseRecorder: httptest.NewRecorder()}, flush: true},
		{name: "hijack", source: &hijackCapabilityWriter{responseProtocolRecorder: &responseProtocolRecorder{}, err: hijackErr}, hijack: true},
		{name: "push", source: &pusherCapabilityWriter{responseProtocolRecorder: &responseProtocolRecorder{}}, push: true},
		{name: "flush-hijack", source: &flusherHijackerCapabilityWriter{optionalCapabilityWriter: &optionalCapabilityWriter{ResponseRecorder: httptest.NewRecorder()}, err: hijackErr}, flush: true, hijack: true},
		{name: "flush-push", source: &flusherPusherCapabilityWriter{optionalCapabilityWriter: &optionalCapabilityWriter{ResponseRecorder: httptest.NewRecorder()}}, flush: true, push: true},
		{name: "hijack-push", source: &hijackerPusherCapabilityWriter{hijackCapabilityWriter: &hijackCapabilityWriter{responseProtocolRecorder: &responseProtocolRecorder{}, err: hijackErr}}, hijack: true, push: true},
		{name: "all", source: &allCapabilityWriter{flusherHijackerCapabilityWriter: &flusherHijackerCapabilityWriter{optionalCapabilityWriter: &optionalCapabilityWriter{ResponseRecorder: httptest.NewRecorder()}, err: hijackErr}}, flush: true, hijack: true, push: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := newStatusTrackingResponseWriter(test.source)
			writer := adaptResponseWriterCapabilities(status, test.source)
			unwrapper, ok := writer.(interface{ Unwrap() stdhttp.ResponseWriter })
			if !ok || unwrapper.Unwrap() != test.source {
				t.Fatalf("facade 必须向 ResponseController 暴露真实能力源: unwrap=%T", writer)
			}
			_, hasFlush := writer.(stdhttp.Flusher)
			_, hasHijack := writer.(stdhttp.Hijacker)
			_, hasPush := writer.(stdhttp.Pusher)
			if hasFlush != test.flush || hasHijack != test.hijack || hasPush != test.push {
				t.Fatalf("能力方法集错误: flush=%t hijack=%t push=%t", hasFlush, hasHijack, hasPush)
			}
			registrar := writer.(interface {
				BeforeCommit(func(stdhttp.Header) error) error
			})
			if err := registrar.BeforeCommit(func(stdhttp.Header) error { return nil }); err != nil {
				t.Fatalf("注册提交钩子失败: %v", err)
			}
			if !writer.(interface{ ResetUncommitted() bool }).ResetUncommitted() {
				t.Fatal("开放状态应允许 Recovery 重置")
			}
			if test.push {
				if err := writer.(stdhttp.Pusher).Push("/asset.js", nil); err != nil {
					t.Fatalf("Push 能力调用失败: %v", err)
				}
			}
			if test.hijack {
				if _, _, err := writer.(stdhttp.Hijacker).Hijack(); !errors.Is(err, hijackErr) {
					t.Fatalf("Hijack 探针错误未透传: %v", err)
				}
			}
			if test.flush {
				if err := stdhttp.NewResponseController(writer).Flush(); err != nil {
					t.Fatalf("Flush 能力调用失败: %v", err)
				}
				// 同时验证 net/http 的无错误返回兼容入口仍委派到统一提交边界。
				writer.(stdhttp.Flusher).Flush()
			} else {
				writer.WriteHeader(stdhttp.StatusOK)
			}
			if status := writer.(interface{ Status() int }).Status(); status != stdhttp.StatusOK {
				t.Fatalf("状态转发错误: %d", status)
			}
			if err := writer.(interface{ CommitError() error }).CommitError(); err != nil {
				t.Fatalf("提交错误转发异常: %v", err)
			}
		})
	}
}
