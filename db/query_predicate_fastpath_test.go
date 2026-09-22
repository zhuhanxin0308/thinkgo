package db

import (
	"reflect"
	"testing"
)

// TestOperationPredicateSingleClausePreservesArguments 验证单条件快速路径保留多占位符参数和原始标记。
func TestOperationPredicateSingleClausePreservesArguments(t *testing.T) {
	database := NewDB(&queryHardeningConnection{})
	query := database.Table("users").WhereBetween("id", 7, 8)
	predicate, err := query.operationPredicate()
	if err != nil {
		t.Fatalf("构造单条件 Predicate 失败: %v", err)
	}
	clauses := predicate.Clauses()
	if len(clauses) != 1 || clauses[0].SQL != "id BETWEEN ? AND ?" {
		t.Fatalf("单条件 Predicate 片段错误: %#v", clauses)
	}
	if !reflect.DeepEqual(clauses[0].Args, []interface{}{7, 8}) {
		t.Fatalf("单条件 Predicate 参数错误: %#v", clauses[0].Args)
	}
	if clauses[0].UnsafeRaw {
		t.Fatal("安全 Where 不应标记为原始条件")
	}

	rawQuery := database.Table("users").WhereRaw("id = ?", 7)
	rawPredicate, err := rawQuery.operationPredicate()
	if err != nil {
		t.Fatalf("构造原始单条件 Predicate 失败: %v", err)
	}
	rawClauses := rawPredicate.Clauses()
	if len(rawClauses) != 1 || !rawClauses[0].UnsafeRaw {
		t.Fatalf("原始单条件标记错误: %#v", rawClauses)
	}
}
