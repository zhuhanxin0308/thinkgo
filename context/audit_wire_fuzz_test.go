package context

import (
	"bufio"
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// FuzzHTTPWireAndProxyGrouping 从真实 HTTP 字节解析开始，检查正文预算和代理头分行等价性。
// 它覆盖 Go HTTP 解析器到框架的边界，不能替代 Nginx 全链路请求走私测试。
func FuzzHTTPWireAndProxyGrouping(f *testing.F) {
	f.Add([]byte("GET / HTTP/1.1\r\nHost: example.com\r\nX-Forwarded-For: 198.51.100.1\r\nX-Forwarded-For: 203.0.113.1\r\nX-Forwarded-Proto: http\r\nX-Forwarded-Proto: https\r\n\r\n"))
	f.Add([]byte("POST / HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n2\r\n{}\r\n0\r\n\r\n"))
	f.Fuzz(func(t *testing.T, wire []byte) {
		const maximumBody = 2048
		if len(wire) > 4*maximumBody {
			return
		}
		raw, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(wire)))
		if err != nil {
			return
		}
		raw.RemoteAddr = "127.0.0.1:12345"
		merged := raw.Clone(t.Context())
		merged.Body = http.NoBody
		merged.ContentLength = 0
		for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Proto"} {
			if values := raw.Header.Values(name); len(values) > 0 {
				merged.Header.Set(name, strings.Join(values, ","))
			}
		}
		request := MustNewRequest(raw, WithTrustedProxies([]string{"127.0.0.1"}), WithMaxBodyBytes(maximumBody), WithMultipartMemoryLimit(1))
		comparison := MustNewRequest(merged, WithTrustedProxies([]string{"127.0.0.1"}))
		if request.Ip() != comparison.Ip() || request.IsSsl() != comparison.IsSsl() {
			t.Fatal("同一代理链仅改变字段分行就改变了信任判定")
		}
		_ = request.Parse()
		if body, err := request.Body(); err == nil && len(body) > maximumBody {
			t.Fatal("HTTP 正文绕过了框架读取上限")
		}
		// 截断输入的底层 Close 可以返回 I/O 错误，但清理必须可重复且不能发生 panic。
		_ = request.Cleanup()
		_ = request.Cleanup()
		_ = comparison.Cleanup()
	})
}
