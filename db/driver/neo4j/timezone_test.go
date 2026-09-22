package neo4j

import (
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

// TestNeo4jTemporalValuesAndDefaults 验证有时区、墙上时间、嵌套属性与默认 UTC 的不同契约。
func TestNeo4jTemporalValuesAndDefaults(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	var nilConnection *Neo4jConnection
	nilConnection.SetLocation(location)
	if nilConnection.Location() != time.UTC {
		t.Fatal("空连接未回退 UTC")
	}
	connection := &Neo4jConnection{}
	connection.SetLocation(nil)
	if connection.Location() != time.UTC {
		t.Fatal("默认时区不是 UTC")
	}
	connection.SetLocation(location)
	if connection.Location() != location {
		t.Fatal("连接未保存时区")
	}
	caps := connection.Capabilities()
	if !caps.MatchedCountKnown || caps.ModifiedCountKnown || len(caps.InsertIDKinds) != 3 {
		t.Fatalf("能力声明错误: %#v", caps)
	}
	if normalizeNeo4jMapInLocation(nil, time.UTC) != nil {
		t.Fatal("空属性没有保持 nil")
	}
	instant := time.Date(2026, time.July, 24, 16, 30, 0, 123000000, time.UTC)
	wallClock := time.Date(2026, time.July, 25, 0, 30, 0, 123000000, time.UTC)
	for _, value := range []interface{}{instant, neo4j.Time(instant), neo4j.LocalDateTime(wallClock)} {
		actual := normalizeNeo4jValueInLocation(value, location).(time.Time)
		if !actual.Equal(instant) || actual.Location() != location {
			t.Fatalf("时间语义错误: %#v -> %v", value, actual)
		}
	}
	row := normalizeNeo4jMapInLocation(map[string]interface{}{"created_at": instant, "nested": map[string]interface{}{"items": []interface{}{instant}}}, location)
	for _, value := range []interface{}{row["created_at"], row["nested"].(map[string]interface{})["items"].([]interface{})[0]} {
		actual := value.(time.Time)
		if !actual.Equal(instant) || actual.Location() != location {
			t.Fatalf("嵌套时间未归一化: %v", actual)
		}
	}
	if actual := normalizeNeo4jValueInLocation(instant.In(location), nil).(time.Time); !actual.Equal(instant) || actual.Location() != time.UTC {
		t.Fatalf("默认归一化未回退 UTC: %v", actual)
	}
}

// TestNeo4jNativeTimePredicate 验证 ORM 时间范围进入 Cypher 时保持应用时区。
func TestNeo4jNativeTimePredicate(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	connection := &Neo4jConnection{}
	database := db.NewDB(connection)
	database.SetLocation(location)
	predicate, err := database.Table("events").WhereTimeAs("happened_at", db.TimestampValueTypeNative, "today").Predicate()
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := connection.compilePredicate(predicate)
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 2 {
		t.Fatalf("半开范围参数数量错误: %#v", params)
	}
	for _, value := range params {
		timestamp, ok := value.(time.Time)
		if !ok || timestamp.Location() != location {
			t.Fatalf("时间参数退化: %#v", params)
		}
	}
}
