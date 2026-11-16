package http

import (
	"bytes"
	"compress/gzip"
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
		if err := writer.Close(); err != nil {
			t.Fatalf("已完成的 406 不应被视为传输失败: %v", err)
		}
		if !errors.Is(writer.NegotiationError(), ErrContentEncodingNotAcceptable) {
			t.Error("无可接受表示应保留明确协商原因")
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
		{http.MethodGet, http.StatusResetContent, ""},
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

// TestCompressionNegotiatesPreEncodedRepresentation 验证预编码正文按请求的编码许可提交。
func TestCompressionNegotiatesPreEncodedRepresentation(t *testing.T) {
	var encoded bytes.Buffer
	gzipWriter := gzip.NewWriter(&encoded)
	if _, err := gzipWriter.Write([]byte("预编码的业务正文")); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, method string
		accept       []string
		codings      []string
		status       int
		rejected     bool
	}{
		{"显式拒绝", http.MethodGet, []string{"br", "GZIP;q=0, identity;q=0"}, []string{"gzip"}, http.StatusOK, true},
		{"通配拒绝", http.MethodGet, []string{"br, *;q=0"}, []string{"gzip"}, http.StatusOK, true},
		{"指定优先于通配", http.MethodGet, []string{"gzip;q=0, *;q=1"}, []string{"gzip"}, http.StatusOK, true},
		{"多层编码有一项拒绝", http.MethodGet, []string{"gzip, br;q=0"}, []string{"gzip", "br"}, http.StatusOK, true},
		{"未列出", http.MethodGet, []string{"br"}, []string{"gzip"}, http.StatusOK, true},
		{"空请求头", http.MethodGet, []string{""}, []string{"gzip"}, http.StatusOK, true},
		{"允许通过", http.MethodGet, []string{"gzip, *;q=0"}, []string{"gzip"}, http.StatusOK, false},
		{"缺少请求头", http.MethodGet, nil, []string{"gzip"}, http.StatusOK, false},
		{"HEAD", http.MethodHead, []string{"gzip;q=0"}, []string{"gzip"}, http.StatusOK, false},
		{"204", http.MethodGet, []string{"gzip;q=0"}, []string{"gzip"}, http.StatusNoContent, false},
		{"304", http.MethodGet, []string{"gzip;q=0"}, []string{"gzip"}, http.StatusNotModified, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, mode := range []string{"写入", "关闭", "刷新"} {
				t.Run(mode, func(t *testing.T) {
					request := httptest.NewRequest(test.method, "http://example.test", nil)
					if test.accept != nil {
						request.Header["Accept-Encoding"] = test.accept
					}
					threshold := 1024
					if mode == "写入" {
						threshold = 1
					}
					recorder := httptest.NewRecorder()
					writer := NewCompressionResponseWriter(recorder, request, threshold, nil)
					for _, coding := range test.codings {
						writer.Header().Add("Content-Encoding", coding)
					}
					writer.Header().Set("ETag", `"encoded"`)
					writer.Header().Set("Cache-Control", "no-transform")
					writer.WriteHeader(test.status)
					hasBody := test.method != http.MethodHead && test.status == http.StatusOK
					if hasBody {
						if _, err := writer.Write(encoded.Bytes()); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "刷新" {
						if err := writer.flushResponse(); err != nil {
							t.Fatal(err)
						}
					}
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					result := recorder.Result()
					defer result.Body.Close()
					if test.rejected {
						if result.StatusCode != http.StatusNotAcceptable || recorder.Body.Len() != 0 ||
							result.Header.Get("Content-Encoding") != "" || result.Header.Get("ETag") != "" ||
							!errors.Is(writer.NegotiationError(), ErrContentEncodingNotAcceptable) {
							t.Fatalf("不可接受的表示应提交无正文 406: %d %v %v", result.StatusCode, result.Header, writer.NegotiationError())
						}
						return
					}
					if result.StatusCode != test.status || result.Header.Get("Content-Encoding") != test.codings[0] ||
						result.Header.Get("ETag") != `"encoded"` || result.Header.Get("Vary") != "Accept-Encoding" ||
						writer.NegotiationError() != nil || hasBody && !bytes.Equal(recorder.Body.Bytes(), encoded.Bytes()) {
						t.Fatalf("已接受的原表示被更改: %d %v", result.StatusCode, result.Header)
					}
				})
			}
		})
	}
}
