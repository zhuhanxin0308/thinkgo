package context

import "testing"

// BenchmarkStrictJSONObject 单独衡量严格 JSON 参数树，避免混入 HTTP 测试工具的构建成本。
func BenchmarkStrictJSONObject(b *testing.B) {
	bodies := []struct {
		name string
		body string
	}{
		{"Checkout", strictJSONCheckoutBody},
		{"Escaped", `{"name":"\u4f60\u597d","items":[{"path":"a\\b\"c","value":1.25e+03}],"enabled":true}`},
	}
	for _, body := range bodies {
		b.Run(body.name, func(b *testing.B) {
			data := []byte(body.body)
			b.Run("Current", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(data)))
				for b.Loop() {
					if _, err := decodeStrictJSONObject(data); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("TokenReference", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(data)))
				for b.Loop() {
					if _, err := referenceStrictJSONValue(data); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
