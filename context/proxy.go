package context

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// RequestOption 定义请求包装器的可选配置。
type RequestOption func(*Request) error

var (
	// ErrInvalidRequestOption 表示调用方传入了空请求选项。
	ErrInvalidRequestOption = errors.New("请求选项不能为空")
	// ErrInvalidTrustedProxy 表示受信代理配置不是合法的 IP 或 CIDR。
	ErrInvalidTrustedProxy = errors.New("受信代理配置非法")
	// ErrInvalidMultipartMemoryLimit 表示 multipart 内存上限不是正整数。
	ErrInvalidMultipartMemoryLimit = errors.New("multipart 内存上限必须在 1 字节到 1GiB 之间")
	// ErrInvalidMaxBodyBytes 表示请求体上限不是正整数。
	ErrInvalidMaxBodyBytes = errors.New("请求体上限必须在 1 字节到 1GiB 之间")
)

const maxRequestMemoryOrBodyBytes int64 = 1 << 30

// TrustedProxySet 是不可变的受信代理网段集合；集合应在应用启动时编译。
type TrustedProxySet struct {
	networks []*net.IPNet
}

// CompileTrustedProxies 编译受信代理配置，返回值可安全复用于多个请求。
func CompileTrustedProxies(entries []string) (*TrustedProxySet, error) {
	configured := append([]string(nil), entries...)
	networks, err := parseTrustedProxies(configured)
	if err != nil {
		return nil, err
	}
	return &TrustedProxySet{networks: networks}, nil
}

// WithTrustedProxies 配置受信代理网段。
// 只有请求来源命中这些网段时，框架才会信任 X-Forwarded-For 和 X-Forwarded-Proto。
func WithTrustedProxies(entries []string) RequestOption {
	trusted, compileErr := CompileTrustedProxies(entries)
	return func(r *Request) error {
		if compileErr != nil {
			return compileErr
		}
		r.trustedProxies = trusted
		return nil
	}
}

// WithTrustedProxySet 将启动期编译好的代理集合传入请求，避免热路径重复解析配置字符串。
func WithTrustedProxySet(set *TrustedProxySet) RequestOption {
	return func(r *Request) error {
		r.trustedProxies = set
		return nil
	}
}

// WithMultipartMemoryLimit 配置 multipart 表单在内存中的最大缓存字节数。
func WithMultipartMemoryLimit(limit int64) RequestOption {
	return func(r *Request) error {
		if limit <= 0 || limit > maxRequestMemoryOrBodyBytes {
			return fmt.Errorf("%w: %d", ErrInvalidMultipartMemoryLimit, limit)
		}
		r.multipartMemoryLimit = limit
		return nil
	}
}

// WithMaxBodyBytes 配置可缓存和解析的最大请求体字节数。
func WithMaxBodyBytes(limit int64) RequestOption {
	return func(r *Request) error {
		if limit <= 0 || limit > maxRequestMemoryOrBodyBytes {
			return fmt.Errorf("%w: %d", ErrInvalidMaxBodyBytes, limit)
		}
		r.maxBodyBytes = limit
		if !r.constructing && r.raw != nil && r.raw.Body != nil && r.raw.Body != http.NoBody {
			remaining := limit
			if r.bodyLimit != nil {
				remaining -= r.bodyLimit.consumed.Load()
			}
			if remaining < 0 {
				remaining = 0
			}
			limited := newBodyLimitReadCloser(r.raw.Body, remaining)
			r.raw.Body = limited
			if r.bodyLimit == nil {
				r.bodyLimit = limited
			}
		}
		return nil
	}
}

// parseTrustedProxies 严格解析受信代理配置，任一非法项都会使整组配置失败。
func parseTrustedProxies(entries []string) ([]*net.IPNet, error) {
	trusted := make([]*net.IPNet, 0, len(entries))
	for index, entry := range entries {
		network, err := parseTrustedProxyEntry(entry)
		if err != nil {
			return nil, fmt.Errorf("%w: 第 %d 项 %q: %v", ErrInvalidTrustedProxy, index+1, entry, err)
		}
		trusted = append(trusted, network)
	}
	return trusted, nil
}

// parseTrustedProxyEntry 支持单个 IP 和 CIDR 两种写法。
func parseTrustedProxyEntry(entry string) (*net.IPNet, error) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil, errors.New("配置项为空")
	}

	if strings.Contains(entry, "/") {
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, err
		}
		return network, nil
	}

	ip := net.ParseIP(entry)
	if ip == nil {
		return nil, errors.New("不是合法 IP 地址")
	}

	maskBits := 128
	if ip.To4() != nil {
		ip = ip.To4()
		maskBits = 32
	}

	return &net.IPNet{
		IP:   ip,
		Mask: net.CIDRMask(maskBits, maskBits),
	}, nil
}

// shouldTrustProxyHeaders 判断当前连接是否来自受信代理。
func shouldTrustProxyHeaders(raw *http.Request, trusted *TrustedProxySet) bool {
	if raw == nil || trusted == nil || len(trusted.networks) == 0 {
		return false
	}

	remoteIP := parseRemoteIP(raw.RemoteAddr)
	if remoteIP == nil {
		return false
	}

	return trusted.contains(remoteIP)
}

// isTrustedProxyIP 判断单个 IP 是否命中受信代理网段。
func isTrustedProxyIP(ip net.IP, trusted *TrustedProxySet) bool {
	if trusted == nil {
		return false
	}
	return trusted.contains(ip)
}

func (trusted *TrustedProxySet) contains(ip net.IP) bool {
	if trusted == nil || ip == nil {
		return false
	}
	for _, network := range trusted.networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// resolveClientIP 在可信代理链路下解析真实客户端 IP，否则回退到直接连接地址。
func resolveClientIP(raw *http.Request, trusted *TrustedProxySet) string {
	if raw == nil {
		return ""
	}
	if shouldTrustProxyHeaders(raw, trusted) {
		// 同名字段行保持接收顺序合并，不能丢弃代理在后续行追加的真实节点。
		forwardedFor := strings.TrimSpace(strings.Join(raw.Header.Values("X-Forwarded-For"), ","))
		if forwardedFor != "" {
			clientIP, valid := resolveForwardedFor(forwardedFor, trusted)
			if valid {
				return clientIP
			}
			// 代理链格式异常时回退直连地址，不能继续信任另一条可伪造请求头。
			return directClientIP(raw)
		}

		// X-Real-IP 是单值回退头，重复字段即使内容相同也不参与信任判断。
		if values := raw.Header.Values("X-Real-IP"); len(values) == 1 {
			realIP := strings.TrimSpace(values[0])
			if net.ParseIP(realIP) != nil {
				return realIP
			}
		}
	}

	return directClientIP(raw)
}

// directClientIP 返回 TCP 直连端地址，代理头不可用时统一走此安全回退路径。
func directClientIP(raw *http.Request) string {
	if raw == nil {
		return ""
	}
	remoteIP := parseRemoteIP(raw.RemoteAddr)
	if remoteIP != nil {
		return remoteIP.String()
	}
	return raw.RemoteAddr
}

// resolveForwardedFor 从右向左剥离受信代理，返回离受信代理最近的非受信客户端 IP。
// 这样即使客户端预先伪造 X-Forwarded-For 首段，也不会覆盖真实来源。
func resolveForwardedFor(value string, trusted *TrustedProxySet) (string, bool) {
	forwardedIPs, valid := parseForwardedIPList(value)
	if !valid || len(forwardedIPs) == 0 {
		return "", false
	}

	for index := len(forwardedIPs) - 1; index >= 0; index-- {
		ip := forwardedIPs[index]
		if !isTrustedProxyIP(ip, trusted) {
			return ip.String(), true
		}
	}

	return forwardedIPs[0].String(), true
}

// parseForwardedIPList 严格解析 X-Forwarded-For，防止忽略异常节点后改变代理链含义。
func parseForwardedIPList(value string) ([]net.IP, bool) {
	parts := strings.Split(value, ",")
	result := make([]net.IP, 0, len(parts))
	for _, part := range parts {
		ip := parseRemoteIP(strings.TrimSpace(part))
		if ip != nil {
			result = append(result, ip)
			continue
		}
		return nil, false
	}
	return result, len(result) > 0
}

// isHTTPS 在原生 TLS 或可信代理声明为 HTTPS 时返回 true。
func isHTTPS(raw *http.Request, trusted *TrustedProxySet) bool {
	if raw == nil {
		return false
	}
	if raw.TLS != nil {
		return true
	}

	if !shouldTrustProxyHeaders(raw, trusted) {
		return false
	}

	values := raw.Header.Values("X-Forwarded-Proto")
	if len(values) == 0 {
		return false
	}
	// 最后字段行的最后值对应可信代理的追加结果，空值或未知协议都不能提升为 HTTPS。
	last := values[len(values)-1]
	proto := strings.TrimSpace(last[strings.LastIndexByte(last, ',')+1:])
	return strings.EqualFold(proto, "https")
}

// parseRemoteIP 兼容 IPv4、IPv6 和带端口的 RemoteAddr。
func parseRemoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = strings.Trim(remoteAddr, "[]")
	}
	return net.ParseIP(host)
}
