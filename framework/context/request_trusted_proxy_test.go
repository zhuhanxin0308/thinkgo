package context

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCompileTrustedProxiesAndExplicitRequestOption 验证代理集合只编译一次，并统一覆盖 IPv4、IPv6、CIDR 和非可信来源。
func TestCompileTrustedProxiesAndExplicitRequestOption(t *testing.T) {
	trusted, err := CompileTrustedProxies([]string{"127.0.0.1", "::1", "10.0.0.0/8"})
	if err != nil || trusted == nil {
		t.Fatalf("编译受信代理集合失败: set=%#v err=%v", trusted, err)
	}

	tests := []struct {
		name       string
		remoteAddr string
		secure     bool
	}{
		{name: "ipv4", remoteAddr: "127.0.0.1:4321", secure: true},
		{name: "ipv6", remoteAddr: "[::1]:4321", secure: true},
		{name: "cidr", remoteAddr: "10.1.2.3:4321", secure: true},
		{name: "untrusted", remoteAddr: "198.51.100.8:4321", secure: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			raw.RemoteAddr = testCase.remoteAddr
			raw.Header.Set("X-Forwarded-Proto", "https")
			wrapped, requestErr := NewRequest(raw, WithTrustedProxySet(trusted))
			if requestErr != nil {
				t.Fatalf("创建请求失败: %v", requestErr)
			}
			if wrapped.IsSsl() != testCase.secure {
				t.Fatalf("代理协议判断错误: secure=%t expected=%t", wrapped.IsSsl(), testCase.secure)
			}
		})
	}

	legacyOption := WithTrustedProxies([]string{"127.0.0.1/32"})
	first := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	first.RemoteAddr = "127.0.0.1:1234"
	first.Header.Set("X-Forwarded-Proto", "https")
	second := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	second.RemoteAddr = "198.51.100.8:1234"
	second.Header.Set("X-Forwarded-Proto", "https")
	firstWrapped, firstErr := NewRequest(first, legacyOption)
	secondWrapped, secondErr := NewRequest(second, legacyOption)
	if firstErr != nil || secondErr != nil || !firstWrapped.IsSsl() || secondWrapped.IsSsl() {
		t.Fatalf("兼容代理选项复用错误: first=%v second=%v firstSSL=%t secondSSL=%t", firstErr, secondErr, firstWrapped.IsSsl(), secondWrapped.IsSsl())
	}

	if _, err := CompileTrustedProxies([]string{"10.0.0.0/33"}); !errors.Is(err, ErrInvalidTrustedProxy) {
		t.Fatalf("非法代理网段应返回 ErrInvalidTrustedProxy，实际为 %v", err)
	}
}

// BenchmarkTrustedProxySetRequest 记录请求复用预编译代理集合时的热路径分配。
func BenchmarkTrustedProxySetRequest(b *testing.B) {
	trusted, err := CompileTrustedProxies([]string{"127.0.0.1/32", "10.0.0.0/8"})
	if err != nil {
		b.Fatal(err)
	}
	option := WithTrustedProxySet(trusted)
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	raw.RemoteAddr = "10.1.2.3:4321"
	raw.Header.Set("X-Forwarded-Proto", "https")
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		wrapped, requestErr := NewRequest(raw, option)
		if requestErr != nil || !wrapped.IsSsl() {
			b.Fatalf("预编译代理集合请求失败: %v", requestErr)
		}
	}
}
