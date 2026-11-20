package contract

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"
)

// TestStrictCounterValueAcceptsOnlyExactInt64 验证所有支持的整数来源都不会发生静默截断或符号溢出。
func TestStrictCounterValueAcceptsOnlyExactInt64(t *testing.T) {
	valid := map[interface{}]int64{
		int(1):               1,
		int8(-2):             -2,
		int16(3):             3,
		int32(-4):            -4,
		int64(math.MaxInt64): math.MaxInt64,
		uint(5):              5,
		uint8(6):             6,
		uint16(7):            7,
		uint32(8):            8,
		uint64(9):            9,
		float32(10):          10,
		float64(-11):         -11,
		json.Number("12"):    12,
	}
	for raw, expected := range valid {
		actual, err := StrictCounterValue(raw)
		if err != nil || actual != expected {
			t.Fatalf("精确整数 %#v 转换错误: actual=%d err=%v", raw, actual, err)
		}
	}
	invalid := []interface{}{
		"1",
		true,
		uint64(math.MaxInt64) + 1,
		1.5,
		math.NaN(),
		math.Inf(1),
		json.Number("1.5"),
	}
	for _, raw := range invalid {
		if _, err := StrictCounterValue(raw); !errors.Is(err, ErrInvalidCounterValue) {
			t.Fatalf("非法计数值 %#v 应返回 ErrInvalidCounterValue，实际为 %v", raw, err)
		}
	}
}

// TestCounterArithmeticDetectsDirectionAndOverflow 验证递增、递减方向及 int64 两端溢出都被拒绝。
func TestCounterArithmeticDetectsDirectionAndOverflow(t *testing.T) {
	if value, err := CheckedCounterAdd(-1, 2); err != nil || value != 1 {
		t.Fatalf("正常递增失败: %d %v", value, err)
	}
	if value, err := CheckedCounterSubtract(1, 2); err != nil || value != -1 {
		t.Fatalf("正常递减失败: %d %v", value, err)
	}
	if _, err := CheckedCounterAdd(math.MaxInt64, 1); !errors.Is(err, ErrCounterOverflow) {
		t.Fatalf("正向加法溢出未识别: %v", err)
	}
	if _, err := CheckedCounterAdd(math.MinInt64, -1); !errors.Is(err, ErrCounterOverflow) {
		t.Fatalf("负向加法溢出未识别: %v", err)
	}
	if _, err := CheckedCounterSubtract(math.MinInt64, 1); !errors.Is(err, ErrCounterOverflow) {
		t.Fatalf("减法溢出未识别: %v", err)
	}
	if _, err := CheckedCounterSubtract(1, -1); !errors.Is(err, ErrInvalidCounterStep) {
		t.Fatalf("负递减步长未识别: %v", err)
	}
}

// TestDecodeCounterJSONRejectsTrailingValues 验证计数解码保留大整数精度并拒绝拼接 JSON。
func TestDecodeCounterJSONRejectsTrailingValues(t *testing.T) {
	value, err := DecodeCounterJSON([]byte("9223372036854775807"))
	if err != nil {
		t.Fatalf("解码 MaxInt64 失败: %v", err)
	}
	if parsed, parseErr := StrictCounterValue(value); parseErr != nil || parsed != math.MaxInt64 {
		t.Fatalf("MaxInt64 精度丢失: parsed=%d err=%v", parsed, parseErr)
	}
	if _, err = DecodeCounterJSON([]byte("1 2")); err == nil {
		t.Fatal("拼接多个 JSON 值必须失败")
	}
	if _, err = DecodeCounterJSON([]byte("{")); err == nil {
		t.Fatal("损坏 JSON 必须失败")
	}
	if _, err = DecodeCounterJSON([]byte("1 invalid")); err == nil {
		t.Fatal("有效数字后的非法尾部必须失败")
	}
}

// TestDriverInputBoundaries 验证零值语义与负值、控制字符的拒绝规则在所有缓存后端一致。
func TestDriverInputBoundaries(t *testing.T) {
	for _, ttl := range []time.Duration{0, time.Second, -time.Nanosecond} {
		if err := ValidateDriverTTL(ttl); (ttl < 0) != errors.Is(err, ErrInvalidDriverTTL) {
			t.Fatalf("TTL 边界错误: %v %v", ttl, err)
		}
	}
	for _, step := range []int64{0, 1, math.MaxInt64, -1} {
		if err := ValidateCounterStep(step); (step < 0) != errors.Is(err, ErrInvalidCounterStep) {
			t.Fatalf("计数步长边界错误: %d %v", step, err)
		}
	}
	for input, invalid := range map[string]bool{"": false, "业务:counter": false, "line\nnext": true, "key\x00": true, "key\u0085": true} {
		if ContainsControlCharacter(input) != invalid {
			t.Fatalf("控制字符边界错误: %q", input)
		}
	}
}

func TestPreciseCacheIntegerRoundTripAndValidation(t *testing.T) {
	cases := []interface{}{
		int64(math.MaxInt64),
		int64(math.MinInt64),
		uint64(math.MaxUint64),
		json.Number("9223372036854775807"),
		json.Number("123456789012345678901234567890"),
	}
	if strconv.IntSize == 64 {
		wide := int64(1)<<53 + 1
		bigPositive := int(wide)
		bigUint := uint(wide)
		cases = append(cases, bigPositive, -bigPositive, bigUint)
	}
	for _, expected := range cases {
		if err := ValidateCacheNumber(expected); err != nil {
			t.Fatalf("合法数字 %#v 被拒绝: %v", expected, err)
		}
		encoded, encoding := EncodePreciseCacheInteger(expected)
		if encoding == "" {
			t.Fatalf("大整数 %#v 未生成精确编码", expected)
		}
		decoded, err := DecodePreciseCacheInteger(encoded, encoding)
		if err != nil || decoded != expected {
			t.Fatalf("精确整数往返错误: input=%#v got=%#v err=%v", expected, decoded, err)
		}
	}
	if encoded, encoding := EncodePreciseCacheInteger(42); encoding != "" || encoded != 42 {
		t.Fatalf("普通安全范围 int 不应改写编码: %#v %q", encoded, encoding)
	}
	if decoded, err := DecodePreciseCacheInteger("plain", ""); err != nil || decoded != "plain" {
		t.Fatalf("空编码直通失败: %#v %v", decoded, err)
	}
	for _, badNumber := range []json.Number{"", "bad", "01", "+1", "1e"} {
		if err := ValidateCacheNumber(badNumber); !errors.Is(err, ErrInvalidCacheValue) {
			t.Fatalf("非法 json.Number %q 未拒绝: %v", badNumber, err)
		}
	}
	for _, corrupt := range []struct {
		value    interface{}
		encoding string
	}{
		{123, PreciseIntegerEncodingPrefix + "int64"},
		{"123", "other:int64"},
		{"123", PreciseIntegerEncodingPrefix + "unknown"},
		{"bad", PreciseIntegerEncodingPrefix + "int64"},
		{"bad", PreciseIntegerEncodingPrefix + "uint64"},
		{"1.5", PreciseIntegerEncodingPrefix + "json-number-wide"},
	} {
		if _, err := DecodePreciseCacheInteger(corrupt.value, corrupt.encoding); !errors.Is(err, ErrCorruptCacheEnvelope) {
			t.Fatalf("损坏整数载荷 (%#v, %q) 未拒绝: %v", corrupt.value, corrupt.encoding, err)
		}
	}
}
