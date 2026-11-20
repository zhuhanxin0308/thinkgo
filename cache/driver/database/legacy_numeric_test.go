package database

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// 升级之前直接写入的十进制 JSON 数字必须按其原始字符读取。
func TestLegacyIntegerSurvivesDatabaseAtomicUpdate(t *testing.T) {
	conn := newRecordingCacheConn()
	store, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatal(err)
	}
	const key = "legacy-counter"
	physical := mustStorageKey(t, store, key)
	const exact int64 = 9007199254740993
	conn.rows[physical] = map[string]interface{}{"key": physical, "value": "9007199254740993", "expiry": int64(0)}
	if err := store.UpdatePreserveTTL(key, func(value interface{}, found bool) (interface{}, bool, error) {
		current, err := strictCounterValue(value)
		if err != nil || !found || current != exact {
			t.Fatalf("旧数字读取错误：value=%#v found=%t err=%v", value, found, err)
		}
		return current + 1, false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if value, found, err := store.Get(key); err != nil || !found || value != exact+1 {
		t.Fatalf("旧计数的后继值错误：value=%#v found=%t err=%v", value, found, err)
	}
	wide := strings.Repeat("9", 350)
	conn.rows[physical]["value"] = wide
	if value, found, err := store.Get(key); err != nil || !found || value != json.Number(wide) {
		t.Fatalf("旧宽整数未精确返回：value=%#v found=%t err=%v", value, found, err)
	}
}

// 旧表允许任意 JSON 对象；精确匹配候选信封的三个字段也不能改变业务值类型。
func TestLegacyJSONObjectWithCodecMarkerRemainsBusinessData(t *testing.T) {
	conn := newRecordingCacheConn()
	store, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatal(err)
	}
	const key = "old-business-map"
	physical := mustStorageKey(t, store, key)
	for _, payload := range []string{
		`{"__thinkgo_db_value":"thinkgo-db-value-v1","value":"123","encoding":"integer:int64"}`,
		`{"__thinkgo_db_value":"thinkgo-db-value-v1","value":"bad","encoding":"other","field":1}`,
	} {
		var want map[string]any
		if err := json.Unmarshal([]byte(payload), &want); err != nil {
			t.Fatal(err)
		}
		conn.rows[physical] = map[string]any{"key": physical, "value": payload, "expiry": int64(0)}
		if got, found, err := store.Get(key); err != nil || !found || !reflect.DeepEqual(got, want) {
			t.Fatalf("合法旧业务对象被重新解释：got=%#v want=%#v found=%t err=%v", got, want, found, err)
		}
		if err := store.UpdatePreserveTTL(key, func(got any, found bool) (any, bool, error) {
			if !found || !reflect.DeepEqual(got, want) {
				t.Fatalf("原子回调收到的旧对象不完整：got=%#v want=%#v", got, want)
			}
			return want, false, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
