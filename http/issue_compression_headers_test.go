package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIssue133CompressionHonorsAllCacheControlFields(t *testing.T) {
	for _, values := range [][]string{
		{"public", "no-transform"},
		{"no-transform", "public"},
		{"public, no-transform"},
		{"public", "max-age=30, No-Transform"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		recorder := httptest.NewRecorder()
		writer := NewCompressionResponseWriter(recorder, req, 1, nil)
		writer.Header().Set("Content-Type", "text/plain")
		for _, value := range values {
			writer.Header().Add("Cache-Control", value)
		}
		if _, err := writer.Write([]byte("example")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if recorder.Header().Get("Content-Encoding") != "" || recorder.Body.String() != "example" {
			t.Fatalf("Cache-Control=%q 仍转换了表示: headers=%v body=%q", values, recorder.Header(), recorder.Body.Bytes())
		}
	}
}

func TestIssue134StrongETagPreservesRepresentationAndConditionalResponse(t *testing.T) {
	handler := newIssueOpenAPIHandler(t)
	var identity []byte
	var etag string
	for _, encoding := range []string{"identity", "gzip", "deflate", "br", "zstd"} {
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		req.Header.Set("Accept-Encoding", encoding)
		recorder := httptest.NewRecorder()
		writer := NewCompressionResponseWriter(recorder, req, 1, nil)
		handler.ServeHTTP(writer, req)
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if encoding == "identity" {
			identity = append([]byte(nil), recorder.Body.Bytes()...)
			etag = recorder.Header().Get("ETag")
			if len(identity) == 0 || etag == "" {
				t.Fatal("基线缺少文档或 ETag")
			}
		}
		if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Encoding") != "" || recorder.Header().Get("ETag") != etag || !bytes.Equal(recorder.Body.Bytes(), identity) {
			t.Fatalf("%s 下强标签与表示不一致: status=%d headers=%v", encoding, recorder.Code, recorder.Header())
		}

		conditional := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		conditional.Header.Set("Accept-Encoding", encoding)
		conditional.Header.Set("If-None-Match", etag)
		cached := httptest.NewRecorder()
		cachedWriter := NewCompressionResponseWriter(cached, conditional, 1, nil)
		handler.ServeHTTP(cachedWriter, conditional)
		if err := cachedWriter.Close(); err != nil {
			t.Fatal(err)
		}
		if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 || cached.Header().Get("ETag") != etag || cached.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%s 条件响应错误: status=%d headers=%v body=%q", encoding, cached.Code, cached.Header(), cached.Body.Bytes())
		}
	}
}

func TestIssue134WeakETagStillAllowsCompression(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	writer := NewCompressionResponseWriter(recorder, req, 1, nil)
	writer.Header().Set("Content-Type", "text/plain")
	writer.Header().Set("ETag", `W/"semantic-v1"`)
	if _, err := writer.Write([]byte("example")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeCompressedTestBody("gzip", recorder.Body.Bytes())
	if err != nil || string(decoded) != "example" || recorder.Header().Get("Content-Encoding") != "gzip" || recorder.Header().Get("ETag") != `W/"semantic-v1"` {
		t.Fatalf("弱标签压缩契约错误: headers=%v body=%q err=%v", recorder.Header(), decoded, err)
	}
}
