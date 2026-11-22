package middleware

import (
	"math"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

// HTTP(S) 来源采用 UTS #46 的非过渡映射、上下文和双向文本校验。
// 浏览器允许下划线及普通标签中的连续连字符，规范化后仍沿用 CORS 的主机字符限制。
var corsIDNA = idna.New(idna.MapForLookup(), idna.BidiRule(), idna.StrictDomainName(false), idna.CheckHyphens(false))

func canonicalCorsDomain(host string) string {
	ascii, err := corsIDNA.ToASCII(host)
	if err != nil || ascii == "" {
		return ""
	}
	// 空的 Punycode 载荷可能解码成空标签；不能因此把配置映射到另一个来源。
	if strings.HasPrefix(ascii, ".") || strings.HasSuffix(ascii, ".") || strings.Contains(ascii, "..") {
		if corsDomainLabelCount(host) != corsDomainLabelCount(ascii) {
			return ""
		}
	}
	for _, char := range ascii {
		if !isCorsASCIIHostCharacter(char) {
			return ""
		}
	}
	// WHATWG 以最后一个标签是否为数字决定是否进入 IPv4 解析，数字溢出也不能回退为域名。
	trimmed := strings.TrimSuffix(ascii, ".")
	last := trimmed[strings.LastIndexByte(trimmed, '.')+1:]
	const decimalDigits = "0123456789"
	hex, prefixed := strings.CutPrefix(last, "0x")
	decimal := last != "" && strings.Trim(last, decimalDigits) == ""
	hexadecimal := prefixed && strings.Trim(hex, decimalDigits+"abcdef") == ""
	if decimal || hexadecimal {
		return parseCorsIPv4(trimmed)
	}
	return ascii
}

// corsDomainLabelCount 将 UTS #46 的四种标签分隔符视为等价。
func corsDomainLabelCount(host string) int {
	count := 0
	for range strings.FieldsFuncSeq(host, func(char rune) bool {
		return char == '.' || char == '。' || char == '．' || char == '｡'
	}) {
		count++
	}
	return count
}

// parseCorsIPv4 允许末段占用余下的地址字节，其余段各占一个字节。
func parseCorsIPv4(host string) string {
	const bitsPerOctet = 8
	var address [net.IPv4len]byte
	parts := strings.SplitN(host, ".", len(address)+1)
	if len(parts) > len(address) {
		return ""
	}
	for index, part := range parts {
		if part == "" {
			return ""
		}
		base := 10
		if digits, ok := strings.CutPrefix(part, "0x"); ok {
			base, part = 16, digits
			if part == "" {
				part = "0"
			}
		} else if len(part) > 1 && part[0] == '0' {
			base = 8
		}
		width := 1
		if index == len(parts)-1 {
			width = len(address) - index
		}
		number, err := strconv.ParseUint(part, base, width*bitsPerOctet)
		if err != nil {
			return ""
		}
		for offset := width - 1; offset >= 0; offset-- {
			address[index+offset] = byte(number & math.MaxUint8)
			number >>= bitsPerOctet
		}
	}
	return netip.AddrFrom4(address).String()
}
