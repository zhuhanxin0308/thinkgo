package http

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCompressionAcceptEncodingFieldGrouping(t *testing.T) {
	constructors := []struct {
		name string
		new  func(http.ResponseWriter, *http.Request, int, map[string]int) *CompressionResponseWriter
	}{
		{name: "public", new: NewCompressionResponseWriter},
		{name: "configured", new: newConfiguredCompressionResponseWriter},
	}
	tests := []struct {
		name  string
		items []string
		want  string
	}{
		{name: "quality", items: []string{"gzip;q=0.3", "br;q=0.7", "zstd;q=0.9"}, want: "zstd"},
		{name: "identity", items: []string{"gzip;q=0", "br;q=0", "identity;q=1"}},
		{name: "wildcard", items: []string{"gzip;q=0", "*;q=0.6", "identity;q=0"}, want: "zstd"},
		{name: "wildcard override", items: []string{"*;q=0.5", "zstd;q=0", "br;q=0.2"}, want: "gzip"},
		{name: "duplicate quality", items: []string{"gzip;q=0.2", "gzip;q=0.8", "br;q=0.4"}, want: "gzip"},
		{name: "invalid quality", items: []string{"gzip;q=NaN", "br;q=1"}, want: "br"},
		{name: "empty first field", items: []string{"", "gzip"}, want: "gzip"},
	}
	for _, constructor := range constructors {
		t.Run(constructor.name, func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					// 每个掩码表示同一列表在相邻条目之间保留逗号或拆为新字段。
					for mask := 0; mask < 1<<(len(test.items)-1); mask++ {
						t.Run(fmt.Sprintf("group-%d", mask), func(t *testing.T) {
							fields := []string{test.items[0]}
							for index, item := range test.items[1:] {
								if mask&(1<<index) != 0 {
									fields = append(fields, item)
								} else {
									fields[len(fields)-1] += "," + item
								}
							}
							req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
							for _, field := range fields {
								req.Header.Add("Accept-Encoding", field)
							}
							recorder := httptest.NewRecorder()
							recorder.Header().Set("Vary", "Origin")
							writer := constructor.new(recorder, req, 1, map[string]int{"gzip": 1, "deflate": 1, "br": 1, "zstd": 1})
							writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
							assertCompressionFieldsResponse(t, writer, recorder, test.want)
							if vary := recorder.Header().Values("Vary"); !reflect.DeepEqual(vary, []string{"Origin", "Accept-Encoding"}) {
								t.Fatalf("合并请求字段后必须保留 Vary: %v", vary)
							}
						})
					}
				})
			}
		})
	}
}

func TestCompressionAcceptEncodingFieldsShareBudgets(t *testing.T) {
	entries := make([]string, maxAcceptEncodingEntries)
	for index := range entries {
		entries[index] = "unknown"
	}
	entries[0] = "gzip"
	tooManyFields := make([]string, maxAcceptEncodingEntries+1)
	tooManyFields[0] = "gzip"
	tests := []struct {
		name   string
		fields []string
		want   string
	}{
		{name: "absent"},
		{name: "empty", fields: []string{""}},
		{name: "multiple empty", fields: []string{"", ""}},
		{name: "exact bytes including comma", fields: []string{"gzip", strings.Repeat(" ", maxAcceptEncodingBytes-len("gzip")-1)}, want: "gzip"},
		{name: "one extra byte from comma", fields: []string{"gzip", strings.Repeat(" ", maxAcceptEncodingBytes-len("gzip"))}},
		{name: "oversized later field", fields: []string{"gzip", strings.Repeat("x", maxAcceptEncodingBytes+1)}},
		{name: "oversized single field", fields: []string{"gzip," + strings.Repeat(" ", maxAcceptEncodingBytes)}},
		{name: "exact entries in two fields", fields: []string{strings.Join(entries[:32], ","), strings.Join(entries[32:], ",")}, want: "gzip"},
		{name: "extra entry in later field", fields: []string{strings.Join(entries[:32], ","), strings.Join(entries[32:], ",") + ",unknown"}},
		{name: "exact field count", fields: entries, want: "gzip"},
		{name: "too many empty fields", fields: tooManyFields},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
			for _, field := range test.fields {
				req.Header.Add("Accept-Encoding", field)
			}
			recorder := httptest.NewRecorder()
			writer := NewCompressionResponseWriter(recorder, req, 1, map[string]int{"gzip": 1})
			writer.Header().Set("Content-Type", "text/plain")
			assertCompressionFieldsResponse(t, writer, recorder, test.want)
		})
	}
}

func TestCompressionMultipleEncodingFieldsPreserveBypass(t *testing.T) {
	tests := []struct {
		name   string
		header string
		values []string
	}{
		{name: "no transform", header: "Cache-Control", values: []string{"public", "no-transform"}},
		{name: "strong etag", header: "ETag", values: []string{`"fixed"`}},
		{name: "image", header: "Content-Type", values: []string{"image/png"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
			req.Header.Add("Accept-Encoding", "identity")
			req.Header.Add("Accept-Encoding", "gzip")
			recorder := httptest.NewRecorder()
			recorder.Header().Set("Vary", "Origin, Accept-Encoding")
			writer := NewCompressionResponseWriter(recorder, req, 1, map[string]int{"gzip": 1})
			if writer.encoding != "gzip" {
				t.Fatalf("测试前提必须从第二行协商到 gzip，实际为 %q", writer.encoding)
			}
			writer.Header().Set("Content-Type", "text/plain")
			writer.Header().Del(test.header)
			for _, value := range test.values {
				writer.Header().Add(test.header, value)
			}
			assertCompressionFieldsResponse(t, writer, recorder, "")
			if got := recorder.Header().Values(test.header); !reflect.DeepEqual(got, test.values) {
				t.Fatalf("旁路响应头必须保留: got=%v want=%v", got, test.values)
			}
			if vary := recorder.Header().Values("Vary"); !reflect.DeepEqual(vary, []string{"Origin, Accept-Encoding"}) {
				t.Fatalf("已有 Accept-Encoding Vary 不应重复: %v", vary)
			}
		})
	}
}

func assertCompressionFieldsResponse(t *testing.T, writer *CompressionResponseWriter, recorder *httptest.ResponseRecorder, want string) {
	t.Helper()
	payload := []byte(strings.Repeat("field-grouping-response-", 32))
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("写入响应失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭压缩响应失败: %v", err)
	}
	if got := recorder.Header().Get("Content-Encoding"); got != want {
		t.Fatalf("编码协商错误: got=%q want=%q", got, want)
	}
	decoded := recorder.Body.Bytes()
	if want != "" {
		var err error
		decoded, err = decodeCompressedTestBody(want, decoded)
		if err != nil {
			t.Fatalf("解码 %s 响应失败: %v", want, err)
		}
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatal("请求字段分组不得改变响应正文")
	}
}
