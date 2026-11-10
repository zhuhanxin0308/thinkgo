package db

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// TestUnixMilliConfiguration 验证显式毫秒配置有效，旧 unix 和默认配置仍使用秒。
func TestUnixMilliConfiguration(t *testing.T) {
	for _, input := range []string{"unix_milli", " UNIX_MILLI "} {
		if err := (Config{Type: "sqlite", TimestampValueType: input}).Validate(); err != nil {
			t.Fatalf("配置校验应接受毫秒模式：%v", err)
		}
		if !isSupportedTimestampValueType(input) || normalizeTimestampValueType(input) != "unix_milli" {
			t.Fatalf("毫秒配置未被接受：%q", input)
		}
	}
	instant := time.Date(2026, time.November, 10, 12, 34, 56, 789123456, time.UTC)
	for _, mode := range []string{"", "unix", "int"} {
		if got := defaultTimestampValue(instant, mode); got != instant.Unix() {
			t.Fatalf("旧模式 %q 的秒语义被改变：%v", mode, got)
		}
	}
}

// TestUnixMilliCalendarDST 验证夏令时当天按实际日界线生成毫秒边界，不固定为二十四小时。
func TestUnixMilliCalendarDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	database := NewDB(&timestampCaptureConnection{})
	database.timestampValueType = "unix_milli"
	database.SetLocation(location)
	database.clock = func() time.Time { return time.Date(2026, time.March, 8, 12, 0, 0, 0, location) }
	query := database.Table("events").WhereTime("created_at", "today")
	start := time.Date(2026, time.March, 8, 0, 0, 0, 0, location)
	end := start.AddDate(0, 0, 1)
	if query.err != nil || !reflect.DeepEqual(query.args, []any{start.UnixMilli(), end.UnixMilli()}) || end.Sub(start) != 23*time.Hour {
		t.Fatalf("夏令时日界线错误：%v %v", query.args, query.err)
	}
}

// TestUnixMilliAutoTimestampTypes 覆盖缺失、空值、整数、文本、窄整数溢出和原值保留。
func TestUnixMilliAutoTimestampTypes(t *testing.T) {
	instant := time.Date(2026, time.November, 10, 12, 34, 56, 789123456, time.FixedZone("UTC+8", 8*60*60))
	ms := instant.UnixMilli()
	for _, test := range []struct {
		name     string
		value    any
		want     any
		overflow bool
	}{
		{"空值", nil, ms, false},
		{"有符号长整数", int64(0), ms, false},
		{"无符号长整数", uint64(0), uint64(ms), false},
		{"空文本", "", strconv.FormatInt(ms, 10), false},
		{"保留显式时间", int64(123), int64(123), false},
		{"保留显式文本", "123", "123", false},
		{"八位溢出", int8(0), nil, true},
		{"十六位溢出", int16(0), nil, true},
		{"三十二位溢出", int32(0), nil, true},
		{"无符号八位溢出", uint8(0), nil, true},
		{"无符号十六位溢出", uint16(0), nil, true},
		{"无符号三十二位溢出", uint32(0), nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := map[string]any{"created_at": test.value}
			err := setAutoTimestamp(data, "created_at", instant, "unix_milli")
			if test.overflow {
				if !errors.Is(err, ErrTimestampOverflow) {
					t.Fatalf("应拒绝溢出：%v", err)
				}
				if !reflect.DeepEqual(data["created_at"], test.value) {
					t.Fatal("失败时不得改写原值")
				}
			} else if err != nil || !reflect.DeepEqual(data["created_at"], test.want) {
				t.Fatalf("毫秒填充值错误：%#v，错误 %v", data, err)
			}
		})
	}
	data := map[string]any{}
	if err := setAutoTimestamp(data, "created_at", instant, "unix_milli"); err != nil || data["created_at"] != ms {
		t.Fatalf("缺失字段应生成毫秒整数：%v %v", data, err)
	}
	for _, value := range []any{uint(0), uint64(0)} {
		if err := setAutoTimestamp(map[string]any{"created_at": value}, "created_at", time.UnixMilli(-1), "unix_milli"); !errors.Is(err, ErrTimestampOverflow) {
			t.Fatalf("负毫秒不能写入无符号整数：%v", err)
		}
	}
	if strconv.IntSize == 64 {
		for _, value := range []any{int(0), uint(0)} {
			data := map[string]any{"created_at": value}
			if err := setAutoTimestamp(data, "created_at", instant, "unix_milli"); err != nil || reflect.ValueOf(data["created_at"]).Convert(reflect.TypeOf(ms)).Int() != ms {
				t.Fatalf("机器整数精度丢失：%v %v", data, err)
			}
		}
	}
}

// TestUnixMilliQueryModelAndConditions 验证实际查询、模型和软删除共用毫秒策略，且日界线保持半开区间。
func TestUnixMilliQueryModelAndConditions(t *testing.T) {
	connection := &timestampCaptureConnection{}
	database := NewDB(connection)
	database.autoTimestamp = true
	database.timestampValueType = "unix_milli"
	instant := time.Date(2026, time.November, 10, 12, 34, 56, 789123456, time.UTC)
	database.clock = func() time.Time { return instant }
	database.SetLocation(time.FixedZone("UTC+8", 8*60*60))
	if _, err := database.Table("events").Insert(map[string]any{"name": "创建"}); err != nil {
		t.Fatal(err)
	}
	if connection.insertData["create_time"] != instant.UnixMilli() || connection.insertData["update_time"] != instant.UnixMilli() {
		t.Fatalf("普通插入精度错误：%v", connection.insertData)
	}
	model := NewModel(database, "events").SoftDelete()
	if _, err := model.Insert(map[string]any{"name": "模型创建"}); err != nil {
		t.Fatal(err)
	}
	if connection.insertData["create_time"] != instant.UnixMilli() {
		t.Fatal("模型插入未继承毫秒模式")
	}
	instant = instant.Add(17 * time.Millisecond)
	if _, err := model.Where("id", 1).Update(map[string]any{"name": "模型更新"}); err != nil {
		t.Fatal(err)
	}
	if connection.updateData["update_time"] != instant.UnixMilli() {
		t.Fatal("模型更新丢失毫秒")
	}
	if _, err := model.Where("id", 1).Delete(); err != nil {
		t.Fatal(err)
	}
	if connection.updateData["delete_time"] != instant.UnixMilli() {
		t.Fatal("软删除时间丢失毫秒")
	}
	explicit := database.Table("events").WhereTime("create_time", "=", instant)
	if explicit.err != nil || !reflect.DeepEqual(explicit.args, []any{instant.UnixMilli()}) {
		t.Fatalf("显式条件单位错误：%v %v", explicit.args, explicit.err)
	}
	day := database.Table("events").WhereTime("create_time", "today")
	start := time.Date(2026, time.November, 10, 0, 0, 0, 0, database.Location())
	if day.err != nil || !reflect.DeepEqual(day.args, []any{start.UnixMilli(), start.AddDate(0, 0, 1).UnixMilli()}) {
		t.Fatalf("日界线单位错误：%v %v", day.args, day.err)
	}
}
