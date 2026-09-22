package context

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// absoluteHost 返回经过严格语法校验、可安全用于绝对 URL 的请求主机。
func (r *Request) absoluteHost() string {
	if r == nil {
		return ""
	}
	host := strings.TrimSpace(r.requestHostValue())
	if host == "" || strings.ContainsAny(host, "\\/@?#\x00\r\n\t ") || !isValidAbsoluteRequestHost(host) {
		return ""
	}
	parsed, err := url.Parse("http://" + host)
	if err != nil || parsed.User != nil || parsed.Host != host || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	hostname := parsed.Hostname()
	if hostname == "" || (net.ParseIP(hostname) == nil && !isValidRequestHostname(hostname)) {
		return ""
	}
	return host
}

func (r *Request) requestHostValue() string {
	if r == nil {
		return ""
	}
	r.metadataMu.RLock()
	if r.hostSet {
		host := r.hostValue
		r.metadataMu.RUnlock()
		return host
	}
	r.metadataMu.RUnlock()
	if r.raw == nil {
		return ""
	}
	return r.raw.Host
}

func requestHostname(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	parsed, err := url.Parse("http://" + host)
	if err != nil || parsed.User != nil {
		return ""
	}
	return parsed.Hostname()
}

func ensureRequestPathPrefix(value string) string {
	if value == "" || strings.HasPrefix(value, "/") {
		return value
	}
	return "/" + value
}

// isValidAbsoluteRequestHost 严格校验绝对 URL 使用的主机和可选端口，拒绝服务名端口及畸形 IPv6 括号。
func isValidAbsoluteRequestHost(host string) bool {
	if strings.HasPrefix(host, "[") {
		closingBracket := strings.IndexByte(host, ']')
		if closingBracket <= 1 {
			return false
		}
		ip := net.ParseIP(host[1:closingBracket])
		if ip == nil || ip.To4() != nil {
			return false
		}
		remainder := host[closingBracket+1:]
		return remainder == "" || strings.HasPrefix(remainder, ":") && isValidRequestPort(remainder[1:])
	}
	if strings.ContainsAny(host, "[]") || strings.Count(host, ":") > 1 {
		return false
	}
	hostname := host
	if parsedHost, port, hasPort := strings.Cut(host, ":"); hasPort {
		if !isValidRequestPort(port) {
			return false
		}
		hostname = parsedHost
	}
	hostname = strings.TrimSuffix(hostname, ".")
	return hostname != "" && (net.ParseIP(hostname) != nil || isValidRequestHostname(hostname))
}

func isValidRequestPort(port string) bool {
	if port == "" {
		return false
	}
	for _, char := range port {
		if char < '0' || char > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	return err == nil && parsed > 0
}

func isValidRequestHostname(hostname string) bool {
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	if hostname == "" || len(hostname) > 253 {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '-' {
				return false
			}
		}
	}
	return true
}
