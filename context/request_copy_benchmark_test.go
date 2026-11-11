package context

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

var requestCopyBenchmarkSink interface{}

// BenchmarkRequestByteSnapshot 单独量测既有字节快照路径，不替代或修改冻结业务基准。
func BenchmarkRequestByteSnapshot(b *testing.B) {
	const payloadBytes = 1 << 20
	payload := make([]byte, payloadBytes)
	b.Run("clone", func(b *testing.B) {
		b.SetBytes(payloadBytes)
		b.ReportAllocs()
		for b.Loop() {
			requestCopyBenchmarkSink = deepCloneRequestValue(payload)
		}
	})
	b.Run("all", func(b *testing.B) {
		request := MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil)).WithRoute(map[string]interface{}{"payload": payload})
		b.SetBytes(payloadBytes)
		b.ReportAllocs()
		for b.Loop() {
			requestCopyBenchmarkSink = request.All()
		}
	})
}
