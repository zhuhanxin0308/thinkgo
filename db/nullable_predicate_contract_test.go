//go:build cgo || integration

package db_test

import (
	"slices"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

// SQLite、MongoDB、Neo4j 共用同一组临时数据与预期行名，检查读取、计数和写入是否一致。
func assertNullablePredicateRows(t *testing.T, database *db.DB, table string) {
	t.Helper()
	fixture := []map[string]any{
		{"name": "missing"}, {"name": "null", "state": nil},
		{"name": "open", "state": "open"}, {"name": "closed", "state": "closed"},
	}
	for _, tc := range []struct {
		name  string
		where func(*db.Query) *db.Query
		want  []string
	}{
		{"neq", func(q *db.Query) *db.Query { return q.Where("state", "!=", "closed") }, []string{"open"}},
		{"not_like", func(q *db.Query) *db.Query { return q.Where("state", "NOT LIKE", "cl%") }, []string{"open"}},
		{"not_in", func(q *db.Query) *db.Query { return q.WhereNotIn("state", []any{"closed"}) }, []string{"open"}},
		{"same_field", func(q *db.Query) *db.Query {
			return q.Where("state", "!=", "closed").Where("state", "NOT LIKE", "op%")
		}, nil},
		{"not_in_mixed", func(q *db.Query) *db.Query { return q.WhereNotIn("state", []any{"closed", nil}) }, nil},
		{"not_in_null", func(q *db.Query) *db.Query { return q.WhereNotIn("state", []any{nil}) }, nil},
		{"not_in_empty", func(q *db.Query) *db.Query { return q.WhereNotIn("state", nil) }, []string{"closed", "missing", "null", "open"}},
		{"in_mixed", func(q *db.Query) *db.Query { return q.WhereIn("state", []any{nil, "open"}) }, []string{"open"}},
		{"in_null", func(q *db.Query) *db.Query { return q.WhereIn("state", []any{nil}) }, nil},
		{"in_empty", func(q *db.Query) *db.Query { return q.WhereIn("state", nil) }, nil},
		{"is_null", func(q *db.Query) *db.Query { return q.WhereNull("state") }, []string{"missing", "null"}},
		{"is_not_null", func(q *db.Query) *db.Query { return q.WhereNotNull("state") }, []string{"closed", "open"}},
		{"nested", func(q *db.Query) *db.Query {
			return q.Where(func(g *db.ConditionGroup) { g.Where("state", "<>", "closed").WhereOr("name", "missing") })
		}, []string{"missing", "open"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, row := range fixture {
				row["batch"] = tc.name
				if _, err := database.Table(table).Insert(row); err != nil {
					t.Fatal(err)
				}
			}
			scope := database.Table(table).Where("batch", tc.name)
			query := tc.where(scope)
			assertNames := func(q *db.Query, want []string) {
				t.Helper()
				rows, err := q.Field("name").Order("name ASC").Select()
				if err != nil {
					t.Fatal(err)
				}
				var names []string
				for _, row := range rows {
					name, ok := row["name"].(string)
					if !ok {
						t.Fatalf("记录名称类型错误: %T", row["name"])
					}
					names = append(names, name)
				}
				if !slices.Equal(names, want) {
					t.Fatalf("匹配行 %v，预期 %v", names, want)
				}
			}
			assertNames(query, tc.want)
			wantCount := int64(len(tc.want))
			if count, err := query.Count(); err != nil || count != wantCount {
				t.Fatalf("计数 %d，预期 %d: %v", count, wantCount, err)
			}
			if updated, err := query.Update(map[string]any{"marked": true}); err != nil || updated != wantCount {
				t.Fatalf("更新 %d 行，预期 %d: %v", updated, wantCount, err)
			}
			assertNames(scope.Where("marked", true), tc.want)
			if deleted, err := query.Delete(); err != nil || deleted != wantCount {
				t.Fatalf("删除 %d 行，预期 %d: %v", deleted, wantCount, err)
			}
			if left, err := scope.Count(); err != nil || left != int64(len(fixture))-wantCount {
				t.Fatalf("剩余 %d 行: %v", left, err)
			}
			assertNames(scope.Where("marked", true), nil)
		})
	}
}
