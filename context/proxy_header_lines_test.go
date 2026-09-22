package context

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProxyHeaderLinesPreserveTrust 验证多行代理链、畸形链和单值回退的安全边界。
func TestProxyHeaderLinesPreserveTrust(t *testing.T) {
	const proxyAddress = "10.0.0.10:4321"
	trusted, err := CompileTrustedProxies([]string{"10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name      string
		remote    string
		forwarded []string
		real      []string
		want      string
	}{
		{name: "多行剥离", forwarded: []string{"198.51.100.250", "203.0.113.8, 10.0.0.5"}, want: "203.0.113.8"},
		{name: "IPv6客户端", forwarded: []string{"2001:db8::8", "10.0.0.5"}, want: "2001:db8::8"},
		{name: "全可信链", forwarded: []string{"10.0.0.4", "10.0.0.5"}, want: "10.0.0.4"},
		{name: "后行非法不采用其他头", forwarded: []string{"203.0.113.8", "invalid"}, real: []string{"203.0.113.9"}, want: "10.0.0.10"},
		{name: "首行空值不采用其他头", forwarded: []string{"", "203.0.113.8"}, real: []string{"203.0.113.9"}, want: "10.0.0.10"},
		{name: "尾行空值", forwarded: []string{"203.0.113.8", ""}, want: "10.0.0.10"},
		{name: "单值回退", real: []string{"203.0.113.9"}, want: "203.0.113.9"},
		{name: "重复RealIP", real: []string{"198.51.100.250", "203.0.113.9"}, want: "10.0.0.10"},
		{name: "重复相同RealIP也拒绝", real: []string{"203.0.113.9", "203.0.113.9"}, want: "10.0.0.10"},
		{name: "RealIP不是列表", real: []string{"198.51.100.250, 203.0.113.9"}, want: "10.0.0.10"},
		{name: "空XFF保留既有回退", forwarded: []string{" "}, real: []string{"203.0.113.9"}, want: "203.0.113.9"},
		{name: "有效XFF优先", forwarded: []string{"203.0.113.8"}, real: []string{"198.51.100.250", "203.0.113.9"}, want: "203.0.113.8"},
		{name: "不可信直连", remote: "198.51.100.10:4321", forwarded: []string{"203.0.113.8", "10.0.0.5"}, want: "198.51.100.10"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			raw.RemoteAddr = proxyAddress
			if testCase.remote != "" {
				raw.RemoteAddr = testCase.remote
			}
			for _, value := range testCase.forwarded {
				raw.Header.Add("X-Forwarded-For", value)
			}
			for _, value := range testCase.real {
				raw.Header.Add("X-Real-IP", value)
			}
			request := MustNewRequest(raw, WithTrustedProxySet(trusted))
			if got := request.Ip(); got != testCase.want {
				t.Fatalf("来源地址错误: got=%q want=%q", got, testCase.want)
			}
		})
	}
}

// TestProxyProtocolHeaderLines 使用最后一个可信追加值，保持原生 TLS 优先语义。
func TestProxyProtocolHeaderLines(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		values  []string
		tls     bool
		trusted bool
		want    bool
	}{
		{name: "缺失", trusted: true},
		{name: "伪造HTTPS随后HTTP", values: []string{"https", "http"}, trusted: true},
		{name: "末行HTTPS", values: []string{"http", " HTTPS "}, trusted: true, want: true},
		{name: "混合多行列表", values: []string{"https, http", "http, https"}, trusted: true, want: true},
		{name: "末行空值", values: []string{"https", ""}, trusted: true},
		{name: "末值非法", values: []string{"https", "ftp"}, trusted: true},
		{name: "不可信来源", values: []string{"https", "https"}},
		{name: "原生TLS优先", values: []string{"http", ""}, tls: true, want: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			raw.RemoteAddr = "10.0.0.10:4321"
			if testCase.tls {
				raw.TLS = &tls.ConnectionState{}
			}
			for _, value := range testCase.values {
				raw.Header.Add("X-Forwarded-Proto", value)
			}
			var options []RequestOption
			if testCase.trusted {
				options = append(options, WithTrustedProxies([]string{"10.0.0.0/24"}))
			}
			if got := MustNewRequest(raw, options...).IsSsl(); got != testCase.want {
				t.Fatalf("协议判断错误: got=%t want=%t", got, testCase.want)
			}
		})
	}
}

// TestForwardedLinePartitionInvariant 穷举短链的换行分组，确保字段行合并不改变语义。
func TestForwardedLinePartitionInvariant(t *testing.T) {
	chain := []string{"198.51.100.250", "203.0.113.8", "10.0.0.4", "10.0.0.5"}
	for partition := 0; partition < 1<<(len(chain)-1); partition++ {
		var lines []string
		start := 0
		for index := 0; index < len(chain)-1; index++ {
			if partition&(1<<index) != 0 {
				lines = append(lines, strings.Join(chain[start:index+1], ", "))
				start = index + 1
			}
		}
		lines = append(lines, strings.Join(chain[start:], ", "))
		raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		raw.RemoteAddr = "10.0.0.10:4321"
		for _, line := range lines {
			raw.Header.Add("X-Forwarded-For", line)
		}
		request := MustNewRequest(raw, WithTrustedProxies([]string{"10.0.0.0/24"}))
		if request.Ip() != "203.0.113.8" {
			t.Errorf("字段分组 %d 改变客户端来源: %q", partition, request.Ip())
		}
	}
}
