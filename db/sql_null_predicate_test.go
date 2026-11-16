package db

import (
	"errors"
	"slices"
	"testing"
)

func TestSQLScalarNullPredicatesMatchRealSQLiteRows(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	if _, err := raw.Exec(`CREATE TABLE nodes (id INTEGER PRIMARY KEY, parent_id INTEGER);
		INSERT INTO nodes (id, parent_id) VALUES (1, NULL), (2, 1), (3, NULL);`); err != nil {
		t.Fatal(err)
	}
	query := database.Table("nodes")
	for _, test := range []struct {
		name  string
		query *Query
		want  []int64
	}{
		{"Where", query.Where("parent_id", nil), []int64{1, 3}},
		{"WhereMap", query.WhereMap(map[string]any{"parent_id": nil}), []int64{1, 3}},
		{"WhereField", query.WhereField("parent_id", "=", nil), []int64{1, 3}},
		{"不等于", query.Where("parent_id", "<>", nil), []int64{2}},
		{"IS", query.Where("parent_id", "IS", nil), []int64{1, 3}},
		{"IS NOT", query.Where("parent_id", "IS NOT", nil), []int64{2}},
		{"显式 IS NULL", query.WhereNull("parent_id"), []int64{1, 3}},
		{"普通等于", query.Where("parent_id", 1), []int64{2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows, err := test.query.Order("id").Select()
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]int64, 0, len(rows))
			for _, row := range rows {
				ids = append(ids, row["id"].(int64))
			}
			if !slices.Equal(ids, test.want) {
				t.Fatalf("SQL NULL 语义错误: got=%v want=%v", ids, test.want)
			}
		})
	}
	if _, _, err := query.Where("parent_id", ">", nil).BuildSelectSQL(); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("NULL 不支持顺序比较: %v", err)
	}
}

func TestSQLNullPolicyPreservesPredicatesWithoutSQLBuilder(t *testing.T) {
	connection := &queryHardeningConnection{selectRows: []map[string]any{{"parent_id": int64(3)}}}
	query := NewDB(connection).Table("nodes").Where("parent_id", ">=", nil)
	rows, err := query.Select()
	if err != nil || len(rows) != 1 {
		t.Fatalf("无 SQL 构造器的驱动应保留原生 NULL 比较: %v, %v", rows, err)
	}
	predicate, err := query.operationPredicate()
	if err != nil || len(predicate.nodes) != 1 || predicate.nodes[0].Operator != ">=" ||
		len(predicate.nodes[0].Values) != 1 || predicate.nodes[0].Values[0] != nil {
		t.Fatalf("原生驱动谓词被 SQL 约束改写: %+v, %v", predicate, err)
	}
}
