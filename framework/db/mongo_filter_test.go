package db

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// TestMongoBuildFilterRejectsUnparseable 验证无法解析的条件会返回错误，
// 而非静默丢弃导致空 filter 引发全表更新/删除。
func TestMongoBuildFilterRejectsUnparseable(t *testing.T) {
	c := &MongoConnection{}

	// 受支持的条件应正常解析。
	filter, err := c.buildFilter([]string{"age >= ?"}, []interface{}{18})
	if err != nil {
		t.Fatalf("受支持条件不应报错: %v", err)
	}
	if len(filter) != 1 {
		t.Fatalf("filter 应包含 1 个条件，实际 %d", len(filter))
	}

	// 无法解析的条件必须报错，避免退化为空 filter。
	if _, err := c.buildFilter([]string{"weird_unparseable_clause"}, nil); err == nil {
		t.Fatalf("无法解析的条件应返回错误，避免空 filter 全表操作")
	}

	// 参数数量不足也应报错而非吞掉条件。
	if _, err := c.buildFilter([]string{"age = ?"}, nil); err == nil {
		t.Fatalf("参数不足应返回错误")
	}
}

// TestMongoBuildFilterParsesNotInAsNin 验证 NOT IN 不会被提前解析成错误字段的 IN 条件。
func TestMongoBuildFilterParsesNotInAsNin(t *testing.T) {
	c := &MongoConnection{}

	filter, err := c.buildFilter([]string{"status NOT IN (?, ?)"}, []interface{}{"draft", "deleted"})
	if err != nil {
		t.Fatalf("NOT IN 条件应可解析: %v", err)
	}

	condition, ok := filter["status"].(bson.M)
	if !ok {
		t.Fatalf("NOT IN 应绑定到 status 字段，实际 filter=%#v", filter)
	}
	values, ok := condition["$nin"].([]interface{})
	if !ok || len(values) != 2 || values[0] != "draft" || values[1] != "deleted" {
		t.Fatalf("NOT IN 应生成 $nin 条件，实际 %#v", condition)
	}
	if _, exists := filter["status NOT"]; exists {
		t.Fatalf("NOT IN 不应被解析成错误字段 status NOT，实际 filter=%#v", filter)
	}
}

// TestMongoBuildFilterParsesNotLikeAsNegatedRegex 验证 NOT LIKE 不会被提前解析成错误字段的 LIKE 条件。
func TestMongoBuildFilterParsesNotLikeAsNegatedRegex(t *testing.T) {
	c := &MongoConnection{}

	filter, err := c.buildFilter([]string{"name NOT LIKE ?"}, []interface{}{"%admin%"})
	if err != nil {
		t.Fatalf("NOT LIKE 条件应可解析: %v", err)
	}

	condition, ok := filter["name"].(bson.M)
	if !ok {
		t.Fatalf("NOT LIKE 应绑定到 name 字段，实际 filter=%#v", filter)
	}
	negated, ok := condition["$not"].(bson.M)
	if !ok {
		t.Fatalf("NOT LIKE 应生成 $not 正则条件，实际 %#v", condition)
	}
	if negated["$regex"] != "^.*admin.*$" || negated["$options"] != "i" {
		t.Fatalf("NOT LIKE 正则条件不正确，实际 %#v", negated)
	}
	if _, exists := filter["name NOT"]; exists {
		t.Fatalf("NOT LIKE 不应被解析成错误字段 name NOT，实际 filter=%#v", filter)
	}
}
