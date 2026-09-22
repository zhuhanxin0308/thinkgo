package context

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type responseTimezonePayload struct {
	CreatedAt time.Time `json:"created_at"`
}

type cyclicTimezonePayload struct {
	Next      *cyclicTimezonePayload `json:"next"`
	CreatedAt time.Time              `json:"created_at"`
}

// TestNormalizeJSONTimesSkipsCopyWithoutTime 验证没有时间字段的普通结果不会被反射复制。
func TestNormalizeJSONTimesSkipsCopyWithoutTime(t *testing.T) {
	input := map[string]interface{}{"name": "thinkgo", "items": []string{"one", "two"}}
	normalized, ok := NormalizeJSONTimes(input).(map[string]interface{})
	if !ok {
		t.Fatalf("普通 JSON 结果类型不应变化: %T", NormalizeJSONTimes(input))
	}
	if reflect.ValueOf(normalized).Pointer() != reflect.ValueOf(input).Pointer() {
		t.Fatal("没有时间字段时不应复制根 map")
	}
}

// TestNormalizeJSONTimesPreservesCycleForJSONEncoder 验证循环对象不会在时间规范化阶段无限递归。
func TestNormalizeJSONTimesPreservesCycleForJSONEncoder(t *testing.T) {
	input := &cyclicTimezonePayload{CreatedAt: time.Date(2026, 7, 30, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))}
	input.Next = input
	normalized := NormalizeJSONTimes(input)
	if _, err := json.Marshal(normalized); err == nil {
		t.Fatal("循环 JSON 应由 encoding/json 返回错误，而不是在规范化阶段递归耗尽栈")
	}
	if normalized.(*cyclicTimezonePayload).CreatedAt.Location() != time.UTC {
		t.Fatal("循环对象中的时间字段仍应规范化为 UTC")
	}
}

// TestJSONResponseNormalizesTimesToUTC 验证 JSON 响应中的时间统一输出为带 Z 的 RFC3339 UTC。
func TestJSONResponseNormalizesTimesToUTC(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}

	value := time.Date(2026, time.July, 25, 0, 30, 0, 0, location)
	response := NewResponse().Json(map[string]interface{}{
		"payload": responseTimezonePayload{CreatedAt: value},
		"items":   []time.Time{value},
	})
	body := string(response.GetBody())
	if !strings.Contains(body, `"created_at":"2026-07-24T16:30:00Z"`) {
		t.Fatalf("结构体时间未统一为 UTC: %s", body)
	}
	if !strings.Contains(body, `"items":["2026-07-24T16:30:00Z"]`) {
		t.Fatalf("数组时间未统一为 UTC: %s", body)
	}
	if strings.Contains(body, "+08:00") {
		t.Fatalf("JSON 响应不应继续暴露应用本地偏移: %s", body)
	}
}

// TestJSONPResponseNormalizesPointerTimesToUTC 验证 JSONP 响应中的指针时间也遵循 UTC 协议。
func TestJSONPResponseNormalizesPointerTimesToUTC(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}

	value := time.Date(2026, time.July, 25, 0, 30, 0, 0, location)
	response := NewResponse().Jsonp("callback", map[string]interface{}{"created_at": &value})
	body := string(response.GetBody())
	if !strings.Contains(body, `callback({"created_at":"2026-07-24T16:30:00Z"});`) {
		t.Fatalf("JSONP 指针时间未统一为 UTC: %s", body)
	}
}
