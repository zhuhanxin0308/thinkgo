package db

import (
	"errors"
	"reflect"
	"testing"
)

// TestPredicateTreePreservesBooleanAndOwnership 验证条件树保留布尔边界，外部快照不能修改查询值。
func TestPredicateTreePreservesBooleanAndOwnership(t *testing.T) {
	database := NewDB(&queryHardeningConnection{})
	query := database.Table("users").Where("tenant", 7).Where(func(group *ConditionGroup) {
		group.Where("age", ">=", 18).WhereOr("role", "admin")
	}).WhereIn("id", []interface{}{1, 2}).WhereNull("deleted_at")
	predicate, err := query.operationPredicate()
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := predicate.Nodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 4 || nodes[0].Field != "tenant" || nodes[1].Operator != "OR" || nodes[2].Operator != "IN" || nodes[3].Operator != "IS NULL" {
		t.Fatalf("条件树结构错误: %#v", nodes)
	}
	if nodes[1].Children[0].Operator != ">=" || nodes[1].Children[1].Field != "role" {
		t.Fatalf("分支丢失: %#v", nodes[1])
	}
	nodes[1].Children[0].Values[0] = 99
	nodes[2].Values[0] = 99
	again, err := predicate.Nodes()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again[2].Values, []interface{}{1, 2}) || again[1].Children[0].Values[0] != 18 {
		t.Fatalf("树快照可变: %#v", again)
	}
	raw, err := database.Table("users").WhereRaw("id = ?", 1).operationPredicate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.PortableNodes(); !errors.Is(err, ErrUnsafeExpression) {
		t.Fatalf("Raw 来源边界丢失: %v", err)
	}
}
