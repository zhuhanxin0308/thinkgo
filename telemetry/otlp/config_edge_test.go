package otlp

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func enabledTelemetryValues() map[string]interface{} {
	return map[string]interface{}{
		"enable":          true,
		"service_name":    "orders",
		"service_version": "1.0.0",
		"environment":     "test",
		"endpoint":        "https://collector.example.com/v1/traces",
	}
}

// TestParseProviderConfigRejectsUnsafeTypesAndBounds 验证配置解析不会把浮点、越界、
// 凭据 URL 或错误集合类型静默转换成可运行配置。
func TestParseProviderConfigRejectsUnsafeTypesAndBounds(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value interface{}
	}{
		{name: "enable type", key: "enable", value: "true"},
		{name: "service name type", key: "service_name", value: 7},
		{name: "empty endpoint", key: "endpoint", value: " "},
		{name: "headers type", key: "headers", value: []string{"Authorization: x"}},
		{name: "timeout lower bound", key: "timeout_ms", value: 0},
		{name: "timeout fractional", key: "timeout_ms", value: 1.5},
		{name: "batch lower bound", key: "batch_timeout_ms", value: 0},
		{name: "batch size lower bound", key: "max_export_batch_size", value: 0},
		{name: "queue lower bound", key: "max_queue_size", value: 0},
		{name: "sample lower bound", key: "sample_ratio", value: -0.1},
		{name: "sample type", key: "sample_ratio", value: "1"},
		{name: "credential endpoint", key: "endpoint", value: "https://user:pass@example.com/v1/traces"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			values := enabledTelemetryValues()
			values[testCase.key] = testCase.value
			if _, err := parseProviderConfig(values, "orders", "1.0.0", "test"); err == nil {
				t.Fatalf("非法 telemetry 配置必须失败: key=%s value=%#v", testCase.key, testCase.value)
			}
		})
	}
	values := enabledTelemetryValues()
	values["headers"] = `{"X-Tenant":"tenant-a"}`
	parsed, err := parseProviderConfig(values, "orders", "1.0.0", "test")
	if err != nil || parsed.Headers["X-Tenant"] != "tenant-a" {
		t.Fatalf("合法 JSON headers 未解析: config=%#v err=%v", parsed, err)
	}
}

// TestTelemetryHeaderDecoderRejectsMalformedJSON 验证 headers JSON 的重复键、尾随数据、
// 非字符串值和大小上限都被拒绝，避免把导出凭据解析成歧义结果。
func TestTelemetryHeaderDecoderRejectsMalformedJSON(t *testing.T) {
	for _, encoded := range []string{
		`[]`,
		`{"Authorization":1}`,
		`{"Authorization":"x","Authorization":"y"}`,
		`{"Authorization":"x"} trailing`,
		`{"Authorization":"x"`,
		strings.Repeat("x", maximumHeaderJSONBytes+1),
	} {
		if _, err := decodeHeaderJSON(encoded); err == nil {
			t.Fatalf("非法 headers JSON 必须失败: %q", encoded[:minTelemetryTestStringLength(len(encoded))])
		}
	}
	if _, err := strictHeaders(map[string]interface{}{"Bad Header": "x"}); err == nil {
		t.Fatal("非法 Header 名称必须被拒绝")
	}
	if _, err := strictHeaders(map[string]interface{}{"X-Test": "line\nfeed"}); err == nil {
		t.Fatal("包含换行的 Header 值必须被拒绝")
	}
}

// TestTelemetryNumericAndEndpointValidationCoversSpecialValues 验证 NaN、Inf、JSON 数字
// 和协议边界不会绕过采样或超时校验。
func TestTelemetryNumericAndEndpointValidationCoversSpecialValues(t *testing.T) {
	for _, value := range []interface{}{math.NaN(), math.Inf(1), json.Number("not-a-number")} {
		if _, err := strictFloat(map[string]interface{}{"sample_ratio": value}, "sample_ratio", 1, 0, 1); err == nil {
			t.Fatalf("特殊浮点值必须被拒绝: %#v", value)
		}
	}
	for _, value := range []interface{}{json.Number("1.2"), json.Number("9223372036854775808"), "1"} {
		if _, err := strictInt(map[string]interface{}{"timeout_ms": value}, "timeout_ms", 10, 1, 100); err == nil {
			t.Fatalf("非法整数值必须被拒绝: %#v", value)
		}
	}
	for _, endpoint := range []string{
		"collector.example.com/v1/traces",
		"ftp://collector.example.com/v1/traces",
		"https://collector.example.com/v1/traces?token=secret",
		"https://collector.example.com/v1/traces#fragment",
	} {
		if err := validateEndpoint(endpoint); err == nil {
			t.Fatalf("非法 OTLP endpoint 必须被拒绝: %q", endpoint)
		}
	}
}

func minTelemetryTestStringLength(length int) int {
	if length < 32 {
		return length
	}
	return 32
}
