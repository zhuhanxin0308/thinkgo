package db

import (
	"context"
	"testing"
	"time"
)

// timestampCaptureConnection 用于捕获自动时间戳写入的数据，验证框架是否按配置输出正确的时间值类型。
type timestampCaptureConnection struct {
	connectionIdentityState
	insertData    map[string]interface{}
	updateData    map[string]interface{}
	lastOperation string
}

func (c *timestampCaptureConnection) Select(context.Context, SelectRequest) ([]map[string]interface{}, error) {
	c.lastOperation = "select"
	return nil, nil
}

func (c *timestampCaptureConnection) Insert(_ context.Context, request InsertRequest) (InsertResult, error) {
	c.lastOperation = "insert"
	c.insertData = cloneCapturedData(request.Data())
	return InsertResult{Affected: 1, ID: int64(1), IDKnown: request.WantsID(), Data: request.Data()}, nil
}

func (c *timestampCaptureConnection) Update(_ context.Context, request UpdateRequest) (UpdateResult, error) {
	c.lastOperation = "update"
	c.updateData = cloneCapturedData(request.Data())
	return UpdateResult{Affected: 1, Data: request.Data()}, nil
}

func (c *timestampCaptureConnection) Delete(context.Context, DeleteRequest) (DeleteResult, error) {
	c.lastOperation = "delete"
	return DeleteResult{Deleted: 1}, nil
}

func (c *timestampCaptureConnection) Count(context.Context, CountRequest) (int64, error) {
	c.lastOperation = "count"
	return 0, nil
}

func (c *timestampCaptureConnection) Close() error {
	return nil
}

func cloneCapturedData(data map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(data))
	for key, value := range data {
		cloned[key] = value
	}
	return cloned
}

// TestQueryAutoTimestampUsesUnixSecondsWhenConfigured 验证 Query 在 unix 模式下自动补齐的时间戳必须是整型 Unix 秒，
// 不能再写入 datetime 字符串。
func TestQueryAutoTimestampUsesUnixSecondsWhenConfigured(t *testing.T) {
	conn := &timestampCaptureConnection{}
	database := NewDB(conn)
	database.autoTimestamp = true
	database.timestampValueType = TimestampValueTypeUnix

	if _, err := database.Table("users").Insert(map[string]interface{}{"username": "tester"}); err != nil {
		t.Fatalf("Insert 不应返回错误，实际为 %v", err)
	}
	if _, ok := conn.insertData["create_time"].(int64); !ok {
		t.Fatalf("create_time 应写入 int64 Unix 时间戳，实际类型为 %T，值为 %#v", conn.insertData["create_time"], conn.insertData["create_time"])
	}
	if _, ok := conn.insertData["update_time"].(int64); !ok {
		t.Fatalf("update_time 应写入 int64 Unix 时间戳，实际类型为 %T，值为 %#v", conn.insertData["update_time"], conn.insertData["update_time"])
	}

	if _, err := database.Table("users").Where("id = ?", 39).Update(map[string]interface{}{"uid": "hzzzzzzn"}); err != nil {
		t.Fatalf("Update 不应返回错误，实际为 %v", err)
	}
	if _, ok := conn.updateData["update_time"].(int64); !ok {
		t.Fatalf("Update 自动补齐的 update_time 应写入 int64 Unix 时间戳，实际类型为 %T，值为 %#v", conn.updateData["update_time"], conn.updateData["update_time"])
	}
}

// TestQueryAutoTimestampUsesUnixSecondsByDefault 验证默认时间戳模式就是 Unix 秒，
// 即使配置缺失也不能回退成 datetime 字符串，否则整型时间列会再次截断。
func TestQueryAutoTimestampUsesUnixSecondsByDefault(t *testing.T) {
	conn := &timestampCaptureConnection{}
	database := NewDB(conn)
	database.autoTimestamp = true

	if _, err := database.Table("users").Where("id = ?", 41).Update(map[string]interface{}{"uid": "hzzzzzzo"}); err != nil {
		t.Fatalf("Update 不应返回错误，实际为 %v", err)
	}
	if _, ok := conn.updateData["update_time"].(int64); !ok {
		t.Fatalf("默认模式下 update_time 应写入 int64 Unix 时间戳，实际类型为 %T，值为 %#v", conn.updateData["update_time"], conn.updateData["update_time"])
	}
}

// TestSoftDeleteUsesUnixSecondsWhenConfigured 验证软删除在 unix 模式下也必须写入整型 Unix 秒，
// 避免 delete_time 与 update_time 出现同类字段类型截断错误。
func TestSoftDeleteUsesUnixSecondsWhenConfigured(t *testing.T) {
	conn := &timestampCaptureConnection{}
	database := NewDB(conn)
	database.autoTimestamp = true
	database.timestampValueType = TimestampValueTypeUnix

	model := NewModel(database, "users").SoftDelete()
	if _, err := model.Where("id = ?", 7).Delete(); err != nil {
		t.Fatalf("软删除不应返回错误，实际为 %v", err)
	}
	if _, ok := conn.updateData["delete_time"].(int64); !ok {
		t.Fatalf("软删除写入的 delete_time 应为 int64 Unix 时间戳，实际类型为 %T，值为 %#v", conn.updateData["delete_time"], conn.updateData["delete_time"])
	}
}

// TestQueryAutoTimestampUsesDatetimeWhenConfigured 验证 datetime 模式下自动补齐的时间戳为字符串格式。
func TestQueryAutoTimestampUsesDatetimeWhenConfigured(t *testing.T) {
	conn := &timestampCaptureConnection{}
	database := NewDB(conn)
	database.autoTimestamp = true
	database.timestampValueType = TimestampValueTypeDateTime

	if _, err := database.Name("users").Insert(map[string]interface{}{"username": "tester"}); err != nil {
		t.Fatalf("Insert 不应返回错误，实际为 %v", err)
	}
	createTime, ok := conn.insertData["create_time"].(string)
	if !ok {
		t.Fatalf("datetime 模式下 create_time 应写入字符串，实际类型为 %T", conn.insertData["create_time"])
	}
	// 验证格式：2006-01-02 15:04:05
	if len(createTime) != 19 {
		t.Fatalf("datetime 格式长度应为 19，实际为 %d，值为 %q", len(createTime), createTime)
	}
}

// TestQueryAutoTimestampUsesDateWhenConfigured 验证 date 模式下自动补齐的时间戳为日期字符串。
func TestQueryAutoTimestampUsesDateWhenConfigured(t *testing.T) {
	conn := &timestampCaptureConnection{}
	database := NewDB(conn)
	database.autoTimestamp = true
	database.timestampValueType = TimestampValueTypeDate

	if _, err := database.Name("users").Insert(map[string]interface{}{"username": "tester"}); err != nil {
		t.Fatalf("Insert 不应返回错误，实际为 %v", err)
	}
	createTime, ok := conn.insertData["create_time"].(string)
	if !ok {
		t.Fatalf("date 模式下 create_time 应写入字符串，实际类型为 %T", conn.insertData["create_time"])
	}
	// 验证格式：2006-01-02
	if len(createTime) != 10 {
		t.Fatalf("date 格式长度应为 10，实际为 %d，值为 %q", len(createTime), createTime)
	}
}

// TestQueryAutoTimestampUsesNativeTimeWhenConfigured 验证 native 模式把自动时间戳交给驱动处理。
func TestQueryAutoTimestampUsesNativeTimeWhenConfigured(t *testing.T) {
	conn := &timestampCaptureConnection{}
	database := NewDB(conn)
	database.autoTimestamp = true
	database.timestampValueType = TimestampValueTypeNative

	if _, err := database.Name("users").Insert(map[string]interface{}{"username": "tester"}); err != nil {
		t.Fatalf("native 模式 Insert 不应返回错误: %v", err)
	}
	for _, field := range []string{"create_time", "update_time"} {
		if _, ok := conn.insertData[field].(time.Time); !ok {
			t.Fatalf("native 模式 %s 应写入 time.Time，实际为 %T", field, conn.insertData[field])
		}
	}
}

// TestNormalizeTimestampValueType 验证各种输入都能正确归一化。
func TestNormalizeTimestampValueType(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"unix", TimestampValueTypeUnix},
		{"int", TimestampValueTypeUnix},
		{"UNIX", TimestampValueTypeUnix},
		{"datetime", TimestampValueTypeDateTime},
		{"DATETIME", TimestampValueTypeDateTime},
		{"timestamp", TimestampValueTypeTimestamp},
		{"date", TimestampValueTypeDate},
		{"native", TimestampValueTypeNative},
		{"  datetime  ", TimestampValueTypeDateTime},
		{"", TimestampValueTypeUnix},        // 空串回退到 unix
		{"invalid", TimestampValueTypeUnix}, // 未知类型回退到 unix
	}
	for _, tc := range cases {
		result := normalizeTimestampValueType(tc.input)
		if result != tc.expected {
			t.Errorf("normalizeTimestampValueType(%q) = %q, 期望 %q", tc.input, result, tc.expected)
		}
	}
}
