// Package operational 统一运维端点的访问白名单解析。
package operational

import (
	"fmt"
	"net/netip"
	"strings"
)

// ParsePrefixes 检查运维网段并统一 IPv4 映射表示，拒绝全网段与重复项。
func ParsePrefixes(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	seen := make(map[netip.Prefix]struct{}, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("CIDR %q 非法", value)
		}
		address, bits := prefix.Addr(), prefix.Bits()
		if address.Is4In6() {
			ipv4 := address.Unmap()
			bits -= address.BitLen() - ipv4.BitLen()
			address = ipv4
		}
		if bits <= 0 {
			return nil, fmt.Errorf("CIDR %q 覆盖全网段或不能等价转换为 IPv4 网段", value)
		}
		prefix = netip.PrefixFrom(address, bits).Masked()
		if _, exists := seen[prefix]; exists {
			return nil, fmt.Errorf("CIDR %q 重复", value)
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}
