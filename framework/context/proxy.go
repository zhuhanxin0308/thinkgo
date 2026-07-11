package context

import (
	"net"
	"net/http"
	"strings"
)

// RequestOption 定义请求包装器的可选配置。
type RequestOption func(*Request)

// WithTrustedProxies 配置受信代理网段。
// 只有请求来源命中这些网段时，框架才会信任 X-Forwarded-For 和 X-Forwarded-Proto。
func WithTrustedProxies(entries []string) RequestOption {
	trusted := parseTrustedProxies(entries)
	return func(r *Request) {
		r.trustedProxies = trusted
	}
}

// WithMultipartMemoryLimit 配置 multipart 表单在内存中的最大缓存字节数。
func WithMultipartMemoryLimit(limit int64) RequestOption {
	return func(r *Request) {
		if limit > 0 {
			r.multipartMemoryLimit = limit
		}
	}
}

// parseTrustedProxies 解析受信代理配置，非法项会被安全忽略。
func parseTrustedProxies(entries []string) []*net.IPNet {
	trusted := make([]*net.IPNet, 0, len(entries))
	for _, entry := range entries {
		network := parseTrustedProxyEntry(entry)
		if network != nil {
			trusted = append(trusted, network)
		}
	}
	return trusted
}

// parseTrustedProxyEntry 支持单个 IP 和 CIDR 两种写法。
func parseTrustedProxyEntry(entry string) *net.IPNet {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil
	}

	if strings.Contains(entry, "/") {
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil
		}
		return network
	}

	ip := net.ParseIP(entry)
	if ip == nil {
		return nil
	}

	maskBits := 128
	if ip.To4() != nil {
		ip = ip.To4()
		maskBits = 32
	}

	return &net.IPNet{
		IP:   ip,
		Mask: net.CIDRMask(maskBits, maskBits),
	}
}

// shouldTrustProxyHeaders 判断当前连接是否来自受信代理。
func shouldTrustProxyHeaders(raw *http.Request, trusted []*net.IPNet) bool {
	if len(trusted) == 0 {
		return false
	}

	remoteIP := parseRemoteIP(raw.RemoteAddr)
	if remoteIP == nil {
		return false
	}

	return isTrustedProxyIP(remoteIP, trusted)
}

// isTrustedProxyIP 判断单个 IP 是否命中受信代理网段。
func isTrustedProxyIP(ip net.IP, trusted []*net.IPNet) bool {
	for _, network := range trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// resolveClientIP 在可信代理链路下解析真实客户端 IP，否则回退到直接连接地址。
func resolveClientIP(raw *http.Request, trusted []*net.IPNet) string {
	if shouldTrustProxyHeaders(raw, trusted) {
		if clientIP := resolveForwardedFor(raw.Header.Get("X-Forwarded-For"), trusted); clientIP != "" {
			return clientIP
		}

		if realIP := strings.TrimSpace(raw.Header.Get("X-Real-IP")); realIP != "" {
			if net.ParseIP(realIP) != nil {
				return realIP
			}
		}
	}

	remoteIP := parseRemoteIP(raw.RemoteAddr)
	if remoteIP != nil {
		return remoteIP.String()
	}
	return raw.RemoteAddr
}

// resolveForwardedFor 从右向左剥离受信代理，返回离受信代理最近的非受信客户端 IP。
// 这样即使客户端预先伪造 X-Forwarded-For 首段，也不会覆盖真实来源。
func resolveForwardedFor(value string, trusted []*net.IPNet) string {
	forwardedIPs := parseForwardedIPList(value)
	if len(forwardedIPs) == 0 {
		return ""
	}

	for index := len(forwardedIPs) - 1; index >= 0; index-- {
		ip := forwardedIPs[index]
		if !isTrustedProxyIP(ip, trusted) {
			return ip.String()
		}
	}

	return forwardedIPs[0].String()
}

// parseForwardedIPList 解析 X-Forwarded-For 中的 IP 列表，忽略空值和非法项。
func parseForwardedIPList(value string) []net.IP {
	parts := strings.Split(value, ",")
	result := make([]net.IP, 0, len(parts))
	for _, part := range parts {
		ip := parseRemoteIP(strings.TrimSpace(part))
		if ip != nil {
			result = append(result, ip)
		}
	}
	return result
}

// isHTTPS 在原生 TLS 或可信代理声明为 HTTPS 时返回 true。
func isHTTPS(raw *http.Request, trusted []*net.IPNet) bool {
	if raw.TLS != nil {
		return true
	}

	if !shouldTrustProxyHeaders(raw, trusted) {
		return false
	}

	proto := strings.TrimSpace(strings.Split(raw.Header.Get("X-Forwarded-Proto"), ",")[0])
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
