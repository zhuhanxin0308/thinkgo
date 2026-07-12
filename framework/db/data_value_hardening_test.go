package db

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

// TestDatabaseCursorComparisonAcrossSupportedTypes 验证游标分页对数字、文本、二进制和时间
// 使用稳定的严格顺序，并拒绝类型漂移、空值和非有限数字。
func TestDatabaseCursorComparisonAcrossSupportedTypes(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		left       interface{}
		right      interface{}
		comparison int
	}{
		{name: "跨整数类型", left: int8(1), right: uint64(2), comparison: -1},
		{name: "整数与小数相等", left: int64(2), right: float32(2), comparison: 0},
		{name: "JSON 数字", left: json.Number("3.5"), right: float64(3), comparison: 1},
		{name: "字符串", left: "alpha", right: "beta", comparison: -1},
		{name: "字符串相等", left: "same", right: "same", comparison: 0},
		{name: "二进制", left: []byte{1, 2}, right: []byte{1, 3}, comparison: -1},
		{name: "时间", left: now, right: now.Add(time.Second), comparison: -1},
		{name: "时间相等", left: now, right: now, comparison: 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			comparison, err := compareDatabaseCursor(testCase.left, testCase.right)
			if err != nil || comparison != testCase.comparison {
				t.Fatalf("游标比较错误: got=%d want=%d err=%v", comparison, testCase.comparison, err)
			}
		})
	}

	invalid := [][2]interface{}{
		{nil, 1},
		{1, "1"},
		{"1", []byte("1")},
		{[]byte("1"), "1"},
		{now, now.Format(time.RFC3339)},
		{struct{}{}, struct{}{}},
		{math.NaN(), 1.0},
	}
	for index, values := range invalid {
		if _, err := compareDatabaseCursor(values[0], values[1]); !errors.Is(err, ErrInvalidDatabaseRow) {
			t.Fatalf("第 %d 个非法游标应返回 ErrInvalidDatabaseRow，实际为 %v", index, err)
		}
	}
}

// TestConditionGroupColumnsExpressionsAndErrors 验证嵌套条件组保留 OR 优先级，
// 字段比较和表达式参数均经过安全校验。
func TestConditionGroupColumnsExpressionsAndErrors(t *testing.T) {
	group := NewConditionGroup().
		WhereColumn("users.creator_id", "=", "users.updater_id").
		WhereExp("users.score", ">", "users.baseline + ?", 5).
		WhereOr("users.status = ?", "active")
	clause, args, err := group.compile()
	if err != nil {
		t.Fatalf("编译条件组失败: %v", err)
	}
	if clause != "(users.creator_id = users.updater_id AND (users.score > users.baseline + ? OR users.status = ?))" {
		t.Fatalf("条件组优先级错误: %q", clause)
	}
	if !reflect.DeepEqual(args, []interface{}{5, "active"}) {
		t.Fatalf("条件组参数错误: %#v", args)
	}

	invalidGroups := []*ConditionGroup{
		NewConditionGroup(),
		NewConditionGroup().WhereColumn("bad field", "=", "id"),
		NewConditionGroup().WhereExp("score", ">", "baseline + ?", 1, 2),
		NewConditionGroup().Where(struct{}{}),
	}
	for index, invalid := range invalidGroups {
		if _, _, err := invalid.compile(); err == nil {
			t.Fatalf("第 %d 个非法条件组必须返回错误", index)
		}
	}
}

// TestTimeRangeAndValueNormalization 验证所有日历范围及显式时间值转换，
// 避免零时间、空值、结构体和 NaN/Inf 进入驱动。
func TestTimeRangeAndValueNormalization(t *testing.T) {
	now := time.Date(2026, time.July, 11, 14, 30, 0, 0, time.UTC)
	ranges := []string{"today", "yesterday", "week", "month", "year"}
	for _, name := range ranges {
		start, end, err := buildTimeRange(name, now)
		if err != nil || !start.Before(end) || end.Nanosecond() != 0 {
			t.Fatalf("时间范围 %s 错误: start=%v end=%v err=%v", name, start, end, err)
		}
	}
	if _, _, err := buildTimeRange("quarter", now); err == nil {
		t.Fatal("未知时间范围必须返回错误")
	}

	validValues := []interface{}{now, "2026-07-11", []byte("2026-07-11"), int64(1), uint32(2), float64(3)}
	for _, value := range validValues {
		if _, err := normalizeTimeValue(value); err != nil {
			t.Fatalf("合法时间条件 %T 不应失败: %v", value, err)
		}
	}
	invalidValues := []interface{}{time.Time{}, nil, struct{}{}, math.NaN(), math.Inf(1), float32(math.Inf(-1))}
	for _, value := range invalidValues {
		if _, err := normalizeTimeValue(value); err == nil {
			t.Fatalf("非法时间条件 %T(%v) 必须返回错误", value, value)
		}
	}
}

// TestAutoTimestampPreservesDeclaredTypes 验证自动时间戳在各整数、字符串、时间和 nil 字段上
// 保持声明类型，并明确报告窄整数溢出和不支持的零值类型。
func TestAutoTimestampPreservesDeclaredTypes(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	cases := []struct {
		name  string
		value interface{}
		want  interface{}
	}{
		{name: "int", value: int(0), want: int(100)},
		{name: "int8", value: int8(0), want: int8(100)},
		{name: "int16", value: int16(0), want: int16(100)},
		{name: "int32", value: int32(0), want: int32(100)},
		{name: "int64", value: int64(0), want: int64(100)},
		{name: "uint", value: uint(0), want: uint(100)},
		{name: "uint8", value: uint8(0), want: uint8(100)},
		{name: "uint16", value: uint16(0), want: uint16(100)},
		{name: "uint32", value: uint32(0), want: uint32(100)},
		{name: "uint64", value: uint64(0), want: uint64(100)},
		{name: "unix string", value: "", want: "100"},
		{name: "time", value: time.Time{}, want: now},
		{name: "nil", value: nil, want: int64(100)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			data := map[string]interface{}{"created_at": testCase.value}
			if err := setAutoTimestamp(data, "created_at", now, TimestampValueTypeUnix); err != nil {
				t.Fatalf("设置自动时间戳失败: %v", err)
			}
			if !reflect.DeepEqual(data["created_at"], testCase.want) {
				t.Fatalf("时间戳类型或值错误: got=%#v want=%#v", data["created_at"], testCase.want)
			}
		})
	}

	data := map[string]interface{}{}
	if err := setAutoTimestamp(data, "created_at", now, TimestampValueTypeDateTime); err != nil || data["created_at"] != "1970-01-01 00:01:40" {
		t.Fatalf("缺失 datetime 字段补齐错误: data=%#v err=%v", data, err)
	}
	data = map[string]interface{}{"created_at": "kept"}
	if err := setAutoTimestamp(data, "created_at", now, TimestampValueTypeDate); err != nil || data["created_at"] != "kept" {
		t.Fatalf("非零时间戳不得覆盖: data=%#v err=%v", data, err)
	}
	if err := setAutoTimestamp(nil, "created_at", now, TimestampValueTypeUnix); !errors.Is(err, ErrInvalidDatabaseConfig) {
		t.Fatalf("nil 数据应返回 ErrInvalidDatabaseConfig，实际为 %v", err)
	}
	if err := setAutoTimestamp(map[string]interface{}{"created_at": int8(0)}, "created_at", time.Unix(1_000, 0), TimestampValueTypeUnix); !errors.Is(err, ErrTimestampOverflow) {
		t.Fatalf("窄整数溢出应返回 ErrTimestampOverflow，实际为 %v", err)
	}
	if err := setAutoTimestamp(map[string]interface{}{"created_at": struct{}{}}, "created_at", now, TimestampValueTypeUnix); !errors.Is(err, ErrInvalidDatabaseConfig) {
		t.Fatalf("不支持的零值类型应返回 ErrInvalidDatabaseConfig，实际为 %v", err)
	}
}

// TestCountValueParsingCoversDriverRepresentations 验证各 SQL 驱动常见数值类型都能精确转为非负 int64。
func TestCountValueParsingCoversDriverRepresentations(t *testing.T) {
	valid := []interface{}{
		int64(7), int(7), int8(7), int16(7), int32(7),
		uint(7), uint8(7), uint16(7), uint32(7), uint64(7),
		float64(7), float32(7), []byte(" 7 "), "7", json.Number("7"),
	}
	for _, value := range valid {
		parsed, err := parseCountValue(value)
		if err != nil || parsed != 7 {
			t.Fatalf("计数类型 %T 解析错误: parsed=%d err=%v", value, parsed, err)
		}
	}
	invalid := []interface{}{
		int64(-1), int(-1), int8(-1), int16(-1), int32(-1),
		uint64(math.MaxUint64), -1.0, 1.5, math.NaN(), math.Inf(1), float32(1.5),
		[]byte("1.5"), "invalid", json.Number("1.5"), true,
	}
	for _, value := range invalid {
		if _, err := parseCountValue(value); !errors.Is(err, ErrInvalidAggregateValue) {
			t.Fatalf("非法计数 %T(%v) 应返回 ErrInvalidAggregateValue，实际为 %v", value, value, err)
		}
	}
}

// TestRelationValueKeyRejectsNonFiniteFloats 验证不可比较的特殊浮点值
// 不会被当成稳定关联键参与预加载分组。
func TestRelationValueKeyRejectsNonFiniteFloats(t *testing.T) {
	for _, value := range []interface{}{math.NaN(), math.Inf(1), float32(math.Inf(-1))} {
		if _, _, err := relationValueKey(value); !errors.Is(err, ErrInvalidRelation) {
			t.Fatalf("非有限关联键 %T(%v) 应返回 ErrInvalidRelation，实际为 %v", value, value, err)
		}
	}
}
