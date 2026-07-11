package db

import (
	"reflect"
	"strings"
	"testing"
)

// TestNeo4jBuildCypherWhereRejectsUnparseable 验证无法解析的条件会返回错误，
// 避免更新/删除时静默丢弃 WHERE 造成整类节点被误操作。
func TestNeo4jBuildCypherWhereRejectsUnparseable(t *testing.T) {
	c := &Neo4jConnection{}

	if _, _, err := c.buildCypherWhere([]string{"weird_unparseable_clause"}, nil); err == nil {
		t.Fatal("Neo4j 无法解析的条件必须返回错误，不能静默跳过")
	}
	if _, _, err := c.buildCypherWhere([]string{"age = ?"}, nil); err == nil {
		t.Fatal("Neo4j 条件参数不足必须返回错误")
	}
}

// TestNeo4jBuildCypherWhereSupportsCollectionAndRangeConditions 验证统一查询层
// 产生的 IN、LIKE、BETWEEN 条件在 Neo4j 中不会退化成空 WHERE。
func TestNeo4jBuildCypherWhereSupportsCollectionAndRangeConditions(t *testing.T) {
	c := &Neo4jConnection{}

	clause, params, err := c.buildCypherWhere(
		[]string{"id IN (?, ?)", "name LIKE ?", "age BETWEEN ? AND ?"},
		[]interface{}{int64(1), int64(2), "%Admin_", 18, 30},
	)
	if err != nil {
		t.Fatalf("Neo4j 应支持上层查询生成的集合与区间条件: %v", err)
	}

	expectedFragments := []string{
		"n.id IN $w0",
		"n.name =~ $w1",
		"n.age >= $w2_start AND n.age <= $w2_end",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(clause, fragment) {
			t.Fatalf("Neo4j WHERE 缺少片段 %q，实际为 %q", fragment, clause)
		}
	}
	if !reflect.DeepEqual(params["w0"], []interface{}{int64(1), int64(2)}) {
		t.Fatalf("IN 参数应聚合为切片，实际为 %#v", params["w0"])
	}
	if params["w1"] != "(?i)^.*Admin.$" {
		t.Fatalf("LIKE 参数应转换为大小写不敏感的安全正则，实际为 %#v", params["w1"])
	}
	if params["w2_start"] != 18 || params["w2_end"] != 30 {
		t.Fatalf("BETWEEN 参数应拆分为起止值，实际为 %#v/%#v", params["w2_start"], params["w2_end"])
	}
}
