package mongo

import (
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestMongoTemporalValuesAndDefaults 验证 BSON、嵌套时间与默认 UTC 语义。
func TestMongoTemporalValuesAndDefaults(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	var nilConnection *MongoConnection
	nilConnection.SetLocation(location)
	if nilConnection.Location() != time.UTC {
		t.Fatal("空连接未回退 UTC")
	}
	connection := &MongoConnection{}
	connection.SetLocation(nil)
	if connection.Location() != time.UTC {
		t.Fatal("默认时区不是 UTC")
	}
	connection.SetLocation(location)
	if connection.Location() != location {
		t.Fatal("连接未保存时区")
	}
	instant := time.Date(2026, time.July, 24, 16, 30, 0, 123000000, time.UTC)
	row := normalizeMongoDocumentWithAliasInLocation(bson.M{
		"created_at": bson.DateTime(instant.UnixMilli()), "nested": bson.M{"updated_at": instant}, "items": bson.A{instant},
	}, "", "", location)
	for _, value := range []interface{}{row["created_at"], row["nested"].(map[string]interface{})["updated_at"], row["items"].([]interface{})[0]} {
		actual, ok := value.(time.Time)
		if !ok || !actual.Equal(instant) || actual.Location() != location {
			t.Fatalf("时间未按时区归一化: %#v", value)
		}
	}
	if actual := normalizeMongoValueInLocation(instant.In(location), nil).(time.Time); !actual.Equal(instant) || actual.Location() != time.UTC {
		t.Fatalf("默认归一化未回退 UTC: %v", actual)
	}
}

// TestMongoNativeTimePredicate 验证 ORM 的时间范围在条件树和 BSON 中保持原生类型。
func TestMongoNativeTimePredicate(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	connection := &MongoConnection{}
	database := db.NewDB(connection)
	database.SetLocation(location)
	predicate, err := database.Table("events").WhereTimeAs("happened_at", db.TimestampValueTypeNative, "today").Predicate()
	if err != nil {
		t.Fatal(err)
	}
	filter, err := connection.compilePredicate(predicate, "", "")
	if err != nil {
		t.Fatal(err)
	}
	bounds := filter["happened_at"].(bson.M)
	for _, key := range []string{"$gte", "$lt"} {
		value, ok := bounds[key].(time.Time)
		if !ok || value.Location() != location {
			t.Fatalf("时间参数退化: %#v", bounds)
		}
	}
}

// TestMongoBuildFilterRejectsOperatorInjection 验证 map 条件值不能成为 Mongo 运算符。
func TestMongoBuildFilterRejectsOperatorInjection(t *testing.T) {
	connection := &MongoConnection{}
	if _, err := connection.buildFilter([]string{"username = ?"}, []interface{}{"alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.buildFilter([]string{"username = ?"}, []interface{}{map[string]interface{}{"$ne": nil}}); err == nil {
		t.Fatal("允许了 map 运算符注入")
	}
}
