package db

import (
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestMongoConnectionNormalizesTemporalValues 验证 MongoDB BSON 时间在应用时区中返回。
func TestMongoConnectionNormalizesTemporalValues(t *testing.T) {
	location := mustTimezoneLocation(t, "Asia/Shanghai")
	connection := &MongoConnection{}
	connection.SetLocation(location)
	if connection.Location() != location {
		t.Fatalf("MongoDB 连接未保存应用时区: %v", connection.Location())
	}

	instant := time.Date(2026, time.July, 24, 16, 30, 0, 123000000, time.UTC)
	row := normalizeMongoDocumentWithAliasInLocation(bson.M{
		"created_at": bson.DateTime(instant.UnixMilli()),
		"nested":     bson.M{"updated_at": instant},
		"items":      bson.A{instant},
	}, "", "", location)

	assertApplicationTime(t, row["created_at"], instant, location)
	nested := row["nested"].(map[string]interface{})
	assertApplicationTime(t, nested["updated_at"], instant, location)
	items := row["items"].([]interface{})
	assertApplicationTime(t, items[0], instant, location)
}

// TestNeo4jConnectionNormalizesTemporalValues 验证 Neo4j 有时区和无时区时间的返回语义。
func TestNeo4jConnectionNormalizesTemporalValues(t *testing.T) {
	location := mustTimezoneLocation(t, "Asia/Shanghai")
	connection := &Neo4jConnection{}
	connection.SetLocation(location)
	if connection.Location() != location {
		t.Fatalf("Neo4j 连接未保存应用时区: %v", connection.Location())
	}

	instant := time.Date(2026, time.July, 24, 16, 30, 0, 123000000, time.UTC)
	assertApplicationTime(t, normalizeNeo4jValueInLocation(instant, location), instant, location)
	assertApplicationTime(t, normalizeNeo4jValueInLocation(neo4j.Time(instant), location), instant, location)

	wallClock := time.Date(2026, time.July, 25, 0, 30, 0, 123000000, time.UTC)
	got := normalizeNeo4jValueInLocation(neo4j.LocalDateTime(wallClock), location)
	assertApplicationTime(t, got, time.Date(2026, time.July, 25, 0, 30, 0, 123000000, location), location)
}

// TestNonSQLTimePredicatesKeepNativeDriverValues 验证 MongoDB 与 Neo4j 不会把原生时间条件退化为字符串。
func TestNonSQLTimePredicatesKeepNativeDriverValues(t *testing.T) {
	location := mustTimezoneLocation(t, "America/New_York")
	mongoDatabase := NewDB(&MongoConnection{})
	mongoDatabase.SetLocation(location)
	mongoQuery := mongoDatabase.Table("events").WhereTimeAs("happened_at", TimestampValueTypeNative, "today")
	mongoPredicate, err := mongoQuery.operationPredicate()
	if err != nil {
		t.Fatalf("构造 MongoDB 时间谓词失败: %v", err)
	}
	mongoClauses, mongoArgs, err := mongoPredicate.compileNonSQL()
	if err != nil {
		t.Fatalf("编译 MongoDB 时间谓词失败: %v", err)
	}
	mongoFilter, err := (&MongoConnection{}).buildFilter(mongoClauses, mongoArgs)
	if err != nil {
		t.Fatalf("解析 MongoDB 时间谓词失败: %v", err)
	}
	mongoBounds := mongoFilter["happened_at"].(bson.M)
	if _, ok := mongoBounds["$gte"].(time.Time); !ok {
		t.Fatalf("MongoDB 起始时间不是原生 time.Time: %#v", mongoBounds)
	}
	if _, ok := mongoBounds["$lt"].(time.Time); !ok {
		t.Fatalf("MongoDB 结束时间不是原生 time.Time: %#v", mongoBounds)
	}

	neoDatabase := NewDB(&Neo4jConnection{})
	neoDatabase.SetLocation(location)
	neoQuery := neoDatabase.Table("events").WhereTimeAs("happened_at", TimestampValueTypeNative, "today")
	neoPredicate, err := neoQuery.operationPredicate()
	if err != nil {
		t.Fatalf("构造 Neo4j 时间谓词失败: %v", err)
	}
	neoClauses, neoArgs, err := neoPredicate.compileNonSQL()
	if err != nil {
		t.Fatalf("编译 Neo4j 时间谓词失败: %v", err)
	}
	_, neoParams, err := (&Neo4jConnection{}).buildCypherWhere(neoClauses, neoArgs)
	if err != nil {
		t.Fatalf("解析 Neo4j 时间谓词失败: %v", err)
	}
	for name, value := range neoParams {
		bounds, ok := value.(time.Time)
		if ok {
			if bounds.Location() != location {
				t.Fatalf("Neo4j 时间参数未使用应用时区: %s=%#v", name, value)
			}
			continue
		}
		values, ok := value.([]interface{})
		if !ok || len(values) != 1 {
			t.Fatalf("Neo4j 时间参数类型错误: %s=%#v", name, value)
		}
		for _, item := range values {
			if timestamp, ok := item.(time.Time); !ok || timestamp.Location() != location {
				t.Fatalf("Neo4j 集合时间参数未使用应用时区: %s=%#v", name, value)
			}
		}
	}
}

// TestDatabaseWriteNormalizesNamedTemporalContainers 验证命名容器中的时间也统一到应用时区。
func TestDatabaseWriteNormalizesNamedTemporalContainers(t *testing.T) {
	location := mustTimezoneLocation(t, "Asia/Shanghai")
	instant := time.Date(2026, time.July, 24, 16, 30, 0, 0, time.UTC)
	data := normalizeDatabaseWriteMap(map[string]interface{}{
		"document": bson.M{"happened_at": instant},
		"items":    bson.A{instant},
	}, location)

	document := data["document"].(bson.M)
	assertApplicationTime(t, document["happened_at"], instant, location)
	items := data["items"].(bson.A)
	assertApplicationTime(t, items[0], instant, location)
}

func assertApplicationTime(t *testing.T, value interface{}, expected time.Time, location *time.Location) {
	t.Helper()
	actual, ok := value.(time.Time)
	if !ok {
		t.Fatalf("时间结果类型错误: %T", value)
	}
	if !actual.Equal(expected) || actual.Location() != location {
		t.Fatalf("时间结果未使用应用时区: got=%#v want=%#v location=%v", actual, expected, location)
	}
}
