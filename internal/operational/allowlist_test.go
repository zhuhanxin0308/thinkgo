package operational

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestParsePrefixesNormalizesMappedAddressesAndNetworkBits(t *testing.T) {
	result, err := ParsePrefixes([]string{" ::ffff:192.0.2.17/120 ", "10.23.4.5/8", "2001:db8::abcd/64", "::ffff:127.0.0.1/128"})
	expected := []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("2001:db8::/64"),
		netip.MustParsePrefix("127.0.0.1/32"),
	}
	if err != nil || !reflect.DeepEqual(result, expected) {
		t.Fatalf("CIDR 规范化结果错误: %v, %v", result, err)
	}
	for _, values := range [][]string{
		{"invalid"},
		{"0.0.0.0/0"},
		{"::/0"},
		{"::ffff:192.0.2.17/96"},
		{"::ffff:192.0.2.17/95"},
		{"::ffff:192.0.2.17/120", "192.0.2.0/24"},
	} {
		if prefixes, err := ParsePrefixes(values); err == nil || prefixes != nil {
			t.Errorf("非法或重复 CIDR 应无部分结果: %v, %v", prefixes, err)
		}
	}
}
