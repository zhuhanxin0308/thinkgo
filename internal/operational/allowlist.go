// Package operational 统一运维端点的访问白名单解析。
package operational

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"math"
	"net/netip"
	"slices"
	"strings"
)

type ipv4Range struct {
	start, end uint32
}

type u128 struct {
	hi, lo uint64
}

type ipv6Range struct {
	start, end u128
}

// ParsePrefixes 检查运维网段并统一 IPv4 映射表示，拒绝单条或互补合并后的全网段及重复项。
func ParsePrefixes(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	seen := make(map[netip.Prefix]struct{}, len(values))
	var v4Ranges []ipv4Range
	var v6Ranges []ipv6Range
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
		if prefix.Addr().Is4() {
			v4Ranges = append(v4Ranges, prefixToIPv4Range(prefix))
		} else {
			v6Ranges = append(v6Ranges, prefixToIPv6Range(prefix))
		}
	}
	if coversFullIPv4(v4Ranges) {
		return nil, fmt.Errorf("CIDR 白名单并集覆盖全网段 IPv4，等价于 0.0.0.0/0")
	}
	if coversFullIPv6(v6Ranges) {
		return nil, fmt.Errorf("CIDR 白名单并集覆盖全网段 IPv6，等价于 ::/0")
	}
	return prefixes, nil
}

func prefixToIPv4Range(prefix netip.Prefix) ipv4Range {
	raw := prefix.Addr().As4()
	start := binary.BigEndian.Uint32(raw[:])
	hostBits := 32 - prefix.Bits()
	mask := (uint32(1) << hostBits) - 1
	return ipv4Range{start: start, end: start | mask}
}

func coversFullIPv4(ranges []ipv4Range) bool {
	if len(ranges) == 0 {
		return false
	}
	slices.SortFunc(ranges, func(left, right ipv4Range) int {
		return cmp.Or(cmp.Compare(left.start, right.start), cmp.Compare(right.end, left.end))
	})
	if ranges[0].start != 0 {
		return false
	}
	curEnd := ranges[0].end
	for _, r := range ranges[1:] {
		if curEnd != math.MaxUint32 && r.start > curEnd+1 {
			return false
		}
		if r.end > curEnd {
			curEnd = r.end
		}
	}
	return curEnd == math.MaxUint32
}

func prefixToIPv6Range(prefix netip.Prefix) ipv6Range {
	raw := prefix.Addr().As16()
	start := u128{
		hi: binary.BigEndian.Uint64(raw[0:8]),
		lo: binary.BigEndian.Uint64(raw[8:16]),
	}
	bits := prefix.Bits()
	end := start
	switch {
	case bits == 128:
	case bits > 64:
		hostBits := 128 - bits
		end.lo |= (uint64(1) << hostBits) - 1
	case bits == 64:
		end.lo = math.MaxUint64
	default:
		hostBits := 64 - bits
		end.hi |= (uint64(1) << hostBits) - 1
		end.lo = math.MaxUint64
	}
	return ipv6Range{start: start, end: end}
}

func compareU128(a, b u128) int {
	return cmp.Or(cmp.Compare(a.hi, b.hi), cmp.Compare(a.lo, b.lo))
}

func coversFullIPv6(ranges []ipv6Range) bool {
	if len(ranges) == 0 {
		return false
	}
	slices.SortFunc(ranges, func(left, right ipv6Range) int {
		return cmp.Or(compareU128(left.start, right.start), compareU128(right.end, left.end))
	})
	if ranges[0].start.hi != 0 || ranges[0].start.lo != 0 {
		return false
	}
	curEnd := ranges[0].end
	for _, r := range ranges[1:] {
		if curEnd.hi == math.MaxUint64 && curEnd.lo == math.MaxUint64 {
			return true
		}
		next := curEnd
		if next.lo == math.MaxUint64 {
			next.hi++
			next.lo = 0
		} else {
			next.lo++
		}
		if compareU128(r.start, next) > 0 {
			return false
		}
		if compareU128(r.end, curEnd) > 0 {
			curEnd = r.end
		}
	}
	return curEnd.hi == math.MaxUint64 && curEnd.lo == math.MaxUint64
}
