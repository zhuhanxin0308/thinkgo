package http

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompressionHonorsIdentityExclusion(t *testing.T) {
	const threshold = 1024
	for _, fields := range [][]string{{"gzip, identity;q=0"}, {"gzip", "identity;q=0"}, {"gzip, *;q=0"}} {
		for _, mode := range []string{"close", "flush", "empty-flush", "large"} {
			t.Run(strings.Join(fields, "|")+"/"+mode, func(t *testing.T) {
				request := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
				request.Header["Accept-Encoding"] = fields
				recorder := httptest.NewRecorder()
				writer := NewCompressionResponseWriter(recorder, request, threshold, nil)
				writer.Header().Set("Content-Type", "text/plain")
				payload := []byte("ok")
				if mode == "large" {
					payload = bytes.Repeat(payload, threshold)
				}
				if mode == "empty-flush" {
					if err := writer.flushResponse(); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := writer.Write(payload); err != nil {
					t.Fatal(err)
				}
				if mode == "flush" {
					if err := writer.flushResponse(); err != nil {
						t.Fatal(err)
					}
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Encoding") != "gzip" {
					t.Fatalf("客户端禁止 identity，实际响应 %d %v", recorder.Code, recorder.Header())
				}
				decoded, err := decodeCompressedTestBody("gzip", recorder.Body.Bytes())
				if err != nil || !bytes.Equal(decoded, payload) {
					t.Fatalf("gzip 正文不完整: %v", err)
				}
			})
		}
	}
}

func TestCompressionIdentityAllowedKeepsSmallResponsesPlain(t *testing.T) {
	for _, accept := range []string{"gzip", "gzip;q=0,identity;q=1", "*;q=0,identity;q=1"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
		request.Header.Set("Accept-Encoding", accept)
		writer := NewCompressionResponseWriter(recorder, request, 1024, nil)
		if _, err := writer.Write([]byte("ok")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if recorder.Header().Get("Content-Encoding") != "" || recorder.Body.String() != "ok" {
			t.Fatalf("允许 identity 时阈值优化失效: %v", recorder.Header())
		}
	}
}

// TestCompressionForbiddenPlainBypass 验证保护原表示的旁路不会发送客户端拒绝的明文。
func TestCompressionForbiddenPlainBypass(t *testing.T) {
	for _, headers := range []map[string]string{
		{"Accept-Encoding": "gzip;q=0,identity;q=0"},
		{"Cache-Control": "no-transform"},
		{"ETag": `"fixed"`},
		{"Content-Type": "image/png"},
		{"Content-Range": "bytes 0-1/2"},
		{"Content-Encoding": "identity"},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
		request.Header.Set("Accept-Encoding", "gzip,identity;q=0")
		writer := NewCompressionResponseWriter(recorder, request, 1024, nil)
		for name, value := range headers {
			if name == "Accept-Encoding" {
				request.Header.Set(name, value)
				writer = NewCompressionResponseWriter(recorder, request, 1024, nil)
			} else {
				writer.Header().Set(name, value)
			}
		}
		_, _ = writer.Write([]byte("ok"))
		if err := writer.Close(); !errors.Is(err, ErrContentEncodingNotAcceptable) {
			t.Error("无可接受表示应返回明确错误")
		}
		if recorder.Code != http.StatusNotAcceptable || recorder.Body.Len() != 0 {
			t.Fatalf("应返回无正文 406，实际 %d %q", recorder.Code, recorder.Body.String())
		}
		if recorder.Header().Get("ETag") != "" || recorder.Header().Get("Content-Range") != "" {
			t.Fatal("406 泄漏了原表示元数据")
		}
	}
}

func TestCompressionIdentityExclusionPreservesBodylessAndEncodedResponses(t *testing.T) {
	for _, test := range []struct {
		method   string
		status   int
		encoding string
	}{
		{http.MethodHead, http.StatusOK, ""},
		{http.MethodGet, http.StatusNoContent, ""},
		{http.MethodGet, http.StatusNotModified, ""},
		{http.MethodGet, http.StatusOK, "gzip"},
	} {
		request := httptest.NewRequest(test.method, "http://example.com", nil)
		request.Header.Set("Accept-Encoding", "gzip,identity;q=0")
		recorder := httptest.NewRecorder()
		writer := NewCompressionResponseWriter(recorder, request, 1024, nil)
		writer.WriteHeader(test.status)
		writer.Header().Set("Content-Encoding", test.encoding)
		if test.encoding != "" {
			_, _ = writer.Write([]byte("already-encoded-fixture"))
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != test.status || recorder.Header().Get("Content-Encoding") != test.encoding {
			t.Fatalf("无实体或已编码响应被改写: %d %v", recorder.Code, recorder.Header())
		}
		if test.encoding != "" && recorder.Body.String() != "already-encoded-fixture" {
			t.Fatal("已有编码被二次转换")
		}
	}
}
