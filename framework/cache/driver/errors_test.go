package driver

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
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
		actual, err := strictCounterValue(raw)
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
		if _, err := strictCounterValue(raw); !errors.Is(err, ErrInvalidCounterValue) {
			t.Fatalf("非法计数值 %#v 应返回 ErrInvalidCounterValue，实际为 %v", raw, err)
		}
	}
}

// TestCounterArithmeticDetectsDirectionAndOverflow 验证递增、递减方向及 int64 两端溢出都被拒绝。
func TestCounterArithmeticDetectsDirectionAndOverflow(t *testing.T) {
	if _, err := checkedCounterAdd(math.MaxInt64, 1); !errors.Is(err, ErrCounterOverflow) {
		t.Fatalf("正向加法溢出未识别: %v", err)
	}
	if _, err := checkedCounterAdd(math.MinInt64, -1); !errors.Is(err, ErrCounterOverflow) {
		t.Fatalf("负向加法溢出未识别: %v", err)
	}
	if _, err := checkedCounterSubtract(math.MinInt64, 1); !errors.Is(err, ErrCounterOverflow) {
		t.Fatalf("减法溢出未识别: %v", err)
	}
	if _, err := checkedCounterSubtract(1, -1); !errors.Is(err, ErrInvalidCounterStep) {
		t.Fatalf("负递减步长未识别: %v", err)
	}
}

// TestDecodeCounterJSONRejectsTrailingValues 验证计数解码保留大整数精度并拒绝拼接 JSON。
func TestDecodeCounterJSONRejectsTrailingValues(t *testing.T) {
	value, err := decodeCounterJSON([]byte("9223372036854775807"))
	if err != nil {
		t.Fatalf("解码 MaxInt64 失败: %v", err)
	}
	if parsed, parseErr := strictCounterValue(value); parseErr != nil || parsed != math.MaxInt64 {
		t.Fatalf("MaxInt64 精度丢失: parsed=%d err=%v", parsed, parseErr)
	}
	if _, err = decodeCounterJSON([]byte("1 2")); err == nil {
		t.Fatal("拼接多个 JSON 值必须失败")
	}
	if _, err = decodeCounterJSON([]byte("{")); err == nil {
		t.Fatal("损坏 JSON 必须失败")
	}
}
