package http

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// TestCompressionAlgorithmsRoundTrip 验证每种声明支持的算法都能生成可解码响应。
func TestCompressionAlgorithmsRoundTrip(t *testing.T) {
	payload := []byte(strings.Repeat("thinkgo-compression-", 32))
	levels := map[string]int{"gzip": 1, "deflate": 1, "br": 1, "zstd": 1}
	for _, algorithm := range []string{"gzip", "deflate", "br", "zstd"} {
		t.Run(algorithm, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
			req.Header.Set("Accept-Encoding", algorithm)
			recorder := httptest.NewRecorder()
			writer := NewCompressionResponseWriter(recorder, req, 1, levels)
			writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
			if _, err := writer.Write(payload); err != nil {
				t.Fatalf("写入 %s 响应失败: %v", algorithm, err)
			}
			if err := writer.Close(); err != nil {
				t.Fatalf("关闭 %s 响应失败: %v", algorithm, err)
			}
			if recorder.Header().Get("Content-Encoding") != algorithm {
				t.Fatalf("Content-Encoding 错误: %q", recorder.Header().Get("Content-Encoding"))
			}
			decoded, err := decodeCompressedTestBody(algorithm, recorder.Body.Bytes())
			if err != nil {
				t.Fatalf("解码 %s 响应失败: %v", algorithm, err)
			}
			if !bytes.Equal(decoded, payload) {
				t.Fatalf("%s 往返内容不一致", algorithm)
			}
		})
	}
}

func decodeCompressedTestBody(algorithm string, body []byte) ([]byte, error) {
	var reader io.Reader
	var closer io.Closer
	switch algorithm {
	case "gzip":
		decoded, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		reader, closer = decoded, decoded
	case "deflate":
		decoded := flate.NewReader(bytes.NewReader(body))
		reader, closer = decoded, decoded
	case "br":
		reader = brotli.NewReader(bytes.NewReader(body))
	case "zstd":
		decoded, err := zstd.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		reader, closer = decoded, decoded.IOReadCloser()
	default:
		return nil, errors.New("未知压缩算法")
	}
	if closer != nil {
		defer closer.Close()
	}
	return io.ReadAll(reader)
}

// TestCompressionBypassAndStreamingContracts 验证禁止转换、无实体状态和显式刷新不会错误压缩。
func TestCompressionBypassAndStreamingContracts(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		headers map[string]string
	}{
		{name: "no content", status: http.StatusNoContent},
		{name: "not modified", status: http.StatusNotModified},
		{name: "partial", status: http.StatusPartialContent},
		{name: "image", status: http.StatusOK, headers: map[string]string{"Content-Type": "image/png"}},
		{name: "binary download", status: http.StatusOK, headers: map[string]string{"Content-Type": "application/octet-stream"}},
		{name: "no transform", status: http.StatusOK, headers: map[string]string{"Cache-Control": "public, no-transform"}},
		{name: "pre encoded", status: http.StatusOK, headers: map[string]string{"Content-Encoding": "custom"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			recorder := httptest.NewRecorder()
			writer := NewCompressionResponseWriter(recorder, req, 1, map[string]int{"gzip": 1})
			for key, value := range test.headers {
				writer.Header().Set(key, value)
			}
			writer.WriteHeader(test.status)
			if test.status != http.StatusNoContent && test.status != http.StatusNotModified {
				if _, err := writer.Write([]byte(strings.Repeat("body", 16))); err != nil {
					t.Fatalf("写入旁路响应失败: %v", err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatalf("关闭旁路响应失败: %v", err)
			}
			if encoding := recorder.Header().Get("Content-Encoding"); encoding == "gzip" {
				t.Fatalf("旁路响应不应被 gzip 压缩")
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	writer := NewCompressionResponseWriter(recorder, req, 1024, map[string]int{"gzip": 1})
	if _, err := writer.Write([]byte("stream")); err != nil {
		t.Fatalf("写入流式响应失败: %v", err)
	}
	writer.Flush()
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭流式响应失败: %v", err)
	}
	if recorder.Header().Get("Content-Encoding") != "" || recorder.Body.String() != "stream" {
		t.Fatalf("小流式响应应原样刷新，encoding=%q body=%q", recorder.Header().Get("Content-Encoding"), recorder.Body.String())
	}
}

// TestCompressionWriterLifecycleAndOptionalInterfaces 验证关闭幂等、关闭后拒绝写入并透传底层能力探测。
func TestCompressionWriterLifecycleAndOptionalInterfaces(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	writer := NewCompressionResponseWriter(recorder, req, 1, nil)
	if writer.Unwrap() != recorder {
		t.Fatal("Unwrap 应返回底层 ResponseWriter")
	}
	if _, _, err := writer.Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("不支持 Hijack 时应返回 http.ErrNotSupported，实际为 %v", err)
	}
	if err := writer.Push("/asset.js", nil); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("不支持 Push 时应返回 http.ErrNotSupported，实际为 %v", err)
	}
	if _, err := writer.Write([]byte("plain")); err != nil {
		t.Fatalf("写入普通响应失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("首次关闭失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("重复关闭应幂等: %v", err)
	}
	if _, err := writer.Write([]byte("late")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("关闭后写入应返回 io.ErrClosedPipe，实际为 %v", err)
	}
	if writer.ResetUncommitted() {
		t.Fatal("已关闭写入器不能重置")
	}
}

type compressionLifecycleWriter struct {
	*httptest.ResponseRecorder
	closeCalls int
	flushCalls int
}

func (w *compressionLifecycleWriter) Close() error {
	w.closeCalls++
	return nil
}

func (w *compressionLifecycleWriter) Flush() {
	w.flushCalls++
	w.ResponseRecorder.Flush()
}

// TestCompressionWriterDoesNotOwnUnderlyingWriter 验证包装器只关闭自己创建的压缩流，并把流式刷新继续传递到底层连接。
func TestCompressionWriterDoesNotOwnUnderlyingWriter(t *testing.T) {
	plainTarget := &compressionLifecycleWriter{ResponseRecorder: httptest.NewRecorder()}
	plainRequest := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	plainWriter := NewCompressionResponseWriter(plainTarget, plainRequest, 1024, nil)
	if _, err := plainWriter.Write([]byte("plain")); err != nil {
		t.Fatalf("写入普通响应失败: %v", err)
	}
	if err := plainWriter.Close(); err != nil {
		t.Fatalf("关闭普通响应包装器失败: %v", err)
	}
	if plainTarget.closeCalls != 0 {
		t.Fatalf("普通响应包装器不得关闭底层 ResponseWriter，实际关闭 %d 次", plainTarget.closeCalls)
	}

	compressedTarget := &compressionLifecycleWriter{ResponseRecorder: httptest.NewRecorder()}
	compressedRequest := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	compressedRequest.Header.Set("Accept-Encoding", "gzip")
	compressedWriter := NewCompressionResponseWriter(compressedTarget, compressedRequest, 1, map[string]int{"gzip": 1})
	if _, err := compressedWriter.Write([]byte(strings.Repeat("stream", 16))); err != nil {
		t.Fatalf("写入压缩流失败: %v", err)
	}
	compressedWriter.Flush()
	if compressedTarget.flushCalls != 1 {
		t.Fatalf("压缩器刷新后必须继续刷新底层连接，实际刷新 %d 次", compressedTarget.flushCalls)
	}
	if err := compressedWriter.Close(); err != nil {
		t.Fatalf("关闭压缩响应包装器失败: %v", err)
	}
	if compressedTarget.closeCalls != 0 {
		t.Fatalf("压缩响应包装器不得关闭底层 ResponseWriter，实际关闭 %d 次", compressedTarget.closeCalls)
	}
}
