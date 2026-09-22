package context_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

var requestJSONSizeBenchmarkSink error

// BenchmarkRequestJSONSizeAndValidity 衡量公共 Parse 对不同大小文档的成功和拒绝成本，保留大文档部分构树后失败的证据。
func BenchmarkRequestJSONSizeAndValidity(b *testing.B) {
	const checkout = `{"sequence":123456789,"customer_id":12345,"items":[{"product_id":123,"quantity":2},{"product_id":456,"quantity":3}]}`
	const smallItemCount, largeItemCount, largeItemDescriptionBytes = 32, 1024, 256
	const smallItem = `{"product_id":123,"quantity":2}`
	largeItem := `{"product_id":123,"quantity":2,"description":"` + strings.Repeat("x", largeItemDescriptionBytes) + `"}`
	for _, fixture := range []struct {
		name string
		body string
	}{
		{"Checkout", checkout},
		{"Items32", `{"items":[` + strings.TrimSuffix(strings.Repeat(smallItem+",", smallItemCount), ",") + `]}`},
		{"Items1024", `{"items":[` + strings.TrimSuffix(strings.Repeat(largeItem+",", largeItemCount), ",") + `]}`},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			for _, variant := range []struct {
				name  string
				body  string
				valid bool
			}{
				{"Valid", fixture.body, true},
				{"Truncated", fixture.body[:len(fixture.body)-1], false},
				{"TrailingGarbage", fixture.body + "!", false},
			} {
				b.Run(variant.name, func(b *testing.B) {
					template := httptest.NewRequest(http.MethodPost, "/json-size", nil)
					template.Header.Set("Content-Type", "application/json")
					template.ContentLength = int64(len(variant.body))
					b.ReportAllocs()
					b.SetBytes(template.ContentLength)
					b.ResetTimer()
					for b.Loop() {
						raw := *template
						raw.Body = io.NopCloser(strings.NewReader(variant.body))
						request := fwcontext.MustNewRequest(&raw)
						err := request.Parse()
						if (err == nil) != variant.valid {
							b.Fatalf("请求体合法性判断错误: valid=%t error=%v", variant.valid, err)
						}
						requestJSONSizeBenchmarkSink = err
					}
					b.ReportMetric(float64(len(variant.body)), "body_bytes")
				})
			}
		})
	}
}
