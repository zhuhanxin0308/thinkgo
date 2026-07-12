package http

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

// TestExactConfigIntegerTypes 验证所有受支持整数来源均无损转换，越界和小数被拒绝。
func TestExactConfigIntegerTypes(t *testing.T) {
	tests := []struct {
		name     string
		value    interface{}
		expected int64
		valid    bool
	}{
		{name: "int", value: int(1), expected: 1, valid: true},
		{name: "int8", value: int8(2), expected: 2, valid: true},
		{name: "int16", value: int16(3), expected: 3, valid: true},
		{name: "int32", value: int32(4), expected: 4, valid: true},
		{name: "int64", value: int64(5), expected: 5, valid: true},
		{name: "uint", value: uint(6), expected: 6, valid: true},
		{name: "uint8", value: uint8(7), expected: 7, valid: true},
		{name: "uint16", value: uint16(8), expected: 8, valid: true},
		{name: "uint32", value: uint32(9), expected: 9, valid: true},
		{name: "uint64", value: uint64(10), expected: 10, valid: true},
		{name: "float32", value: float32(11), expected: 11, valid: true},
		{name: "float64", value: float64(12), expected: 12, valid: true},
		{name: "json number", value: json.Number("13"), expected: 13, valid: true},
		{name: "string", value: "14", expected: 14, valid: true},
		{name: "fraction", value: 1.5, valid: false},
		{name: "nan", value: math.NaN(), valid: false},
		{name: "uint overflow", value: uint64(math.MaxUint64), valid: false},
		{name: "object", value: struct{}{}, valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := exactConfigInteger(test.value)
			if test.valid && (err != nil || actual != test.expected) {
				t.Fatalf("整数转换错误，期望 %d，实际 %d，错误=%v", test.expected, actual, err)
			}
			if !test.valid && err == nil {
				t.Fatalf("非法值 %#v 必须返回错误", test.value)
			}
		})
	}
}

// TestHTTPConfigCollectionParsing 验证列表、压缩等级和 Host 规范化不会忽略非法项。
func TestHTTPConfigCollectionParsing(t *testing.T) {
	values := map[string]interface{}{
		"strings":    []string{" a ", "b"},
		"interfaces": []interface{}{"c", "d"},
		"csv":        "e,f",
	}
	for key, expected := range map[string][]string{
		"strings": {"a", "b"}, "interfaces": {"c", "d"}, "csv": {"e", "f"},
	} {
		actual, err := configStringList(values, key, nil)
		if err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("列表 %s 解析错误，实际=%#v 错误=%v", key, actual, err)
		}
	}
	if _, err := configStringList(map[string]interface{}{"bad": []interface{}{1}}, "bad", nil); err == nil {
		t.Fatal("非字符串列表项必须返回错误")
	}
	if _, err := splitStrictConfigList("a,,b", "list"); err == nil {
		t.Fatal("CSV 空项必须返回错误")
	}

	interfaceLevels, err := compressionLevelMap(map[string]interface{}{"gzip": 1})
	if err != nil || interfaceLevels["gzip"] != 1 {
		t.Fatalf("interface 压缩等级解析错误: %#v %v", interfaceLevels, err)
	}
	integerLevels, err := compressionLevelMap(map[string]int{"br": 2})
	if err != nil || integerLevels["br"] != 2 {
		t.Fatalf("int 压缩等级解析错误: %#v %v", integerLevels, err)
	}
	if _, err := compressionLevelMap([]int{1}); err == nil {
		t.Fatal("非对象压缩等级必须返回错误")
	}
	for algorithm, levels := range map[string][]int64{
		"gzip": {-2, 9}, "deflate": {-2, 9}, "br": {0, 11}, "zstd": {1, 4},
	} {
		for _, level := range levels {
			if !validCompressionLevel(algorithm, level) {
				t.Fatalf("算法 %s 的边界等级 %d 应合法", algorithm, level)
			}
		}
	}
	if validCompressionLevel("unknown", 1) {
		t.Fatal("未知压缩算法必须被拒绝")
	}
}

// TestAllowedHostNormalization 验证端口、通配子域和 IPv6 Host 使用统一规范化结果。
func TestAllowedHostNormalization(t *testing.T) {
	hosts, err := normalizeAllowedHosts([]string{"Example.COM:8443", "*.API.Example.com", "[::1]", "example.com:8443"})
	if err != nil {
		t.Fatalf("规范化 Host 白名单失败: %v", err)
	}
	if !reflect.DeepEqual(hosts, []string{"example.com", "*.api.example.com", "::1"}) {
		t.Fatalf("Host 白名单规范化结果错误: %#v", hosts)
	}
	handler := &Http{srvConf: serverConf{AllowedHosts: hosts}}
	for _, host := range []string{"example.com:9000", "example.com.:443", "v1.api.example.com", "[::1]:8080"} {
		if !handler.isAllowedHost(host) {
			t.Fatalf("Host %q 应命中白名单", host)
		}
	}
	for _, host := range []string{
		"api.example.com",
		"evil.example.net",
		"bad@host",
		"example.com:not-a-port",
		"example.com:65536",
		"[[::1]]",
		"::1",
	} {
		if handler.isAllowedHost(host) {
			t.Fatalf("Host %q 不应命中白名单", host)
		}
	}
	if _, err := normalizeAllowedHosts([]string{"*.bad_host"}); err == nil {
		t.Fatal("非法通配 Host 必须返回错误")
	}
}
