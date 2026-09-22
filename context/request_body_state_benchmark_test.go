package context_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// BenchmarkRequestBodyState 单独观察正文同步对空 GET、不透明正文和表单解析的影响。
func BenchmarkRequestBodyState(b *testing.B) {
	for _, testCase := range []struct {
		name, method, media, body string
	}{
		{"GETParse", http.MethodGet, "", ""},
		{"OpaqueParse", http.MethodPost, "application/octet-stream", "request-body"},
		{"FormParse", http.MethodPost, "application/x-www-form-urlencoded", "name=alice&role=user"},
	} {
		b.Run(testCase.name, func(b *testing.B) {
			template := httptest.NewRequest(testCase.method, "/body", nil)
			template.Header.Set("Content-Type", testCase.media)
			template.ContentLength = int64(len(testCase.body))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				raw := *template
				if testCase.body != "" {
					raw.Body = io.NopCloser(strings.NewReader(testCase.body))
				}
				request := fwcontext.MustNewRequest(&raw)
				if err := request.Parse(); err != nil {
					b.Fatal(err)
				}
				requestJSONBenchmarkSink = request
			}
		})
	}
}
