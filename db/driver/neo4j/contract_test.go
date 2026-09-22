package neo4j

import (
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

var (
	NewDB               = db.NewDB
	newSelectRequest    = db.NewSelectRequest
	newInsertRequest    = db.NewInsertRequest
	newUpdateRequest    = db.NewUpdateRequest
	newDeleteRequest    = db.NewDeleteRequest
	newCountRequest     = db.NewCountRequest
	ErrUnsafeExpression = db.ErrUnsafeExpression
)

func newPredicate() Predicate { return Predicate{} }

func mustTestPredicate(t *testing.T, clauses []string, args []interface{}) Predicate {
	t.Helper()
	predicate, err := db.ParsePredicate(clauses, args)
	if err != nil {
		t.Fatal(err)
	}
	return predicate
}

// buildCypherWhere 将历史文本用例送入公共输入边界，实际驱动只编译条件树。
func (c *Neo4jConnection) buildCypherWhere(where []string, args []interface{}) (string, map[string]interface{}, error) {
	predicate, err := db.ParsePredicate(where, args)
	if err != nil {
		return "", nil, err
	}
	return c.compilePredicate(predicate)
}
