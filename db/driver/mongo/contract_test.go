package mongo

import (
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"go.mongodb.org/mongo-driver/v2/bson"
)

var (
	NewDB               = db.NewDB
	NewModel            = db.NewModel
	newSelectRequest    = db.NewSelectRequest
	newInsertRequest    = db.NewInsertRequest
	newUpdateRequest    = db.NewUpdateRequest
	newDeleteRequest    = db.NewDeleteRequest
	newCountRequest     = db.NewCountRequest
	ErrInvalidModel     = db.ErrInvalidModel
	ErrUnsafeExpression = db.ErrUnsafeExpression
	cloneDatabaseMap    = db.CloneDriverData
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

// buildFilter 将历史文本用例送入公共输入边界，实际驱动始终只接收结构化条件。
func (c *MongoConnection) buildFilter(where []string, args []interface{}) (bson.M, error) {
	return c.buildFilterWithPrimaryKey(where, args, "")
}

func (c *MongoConnection) buildFilterWithPrimaryKey(where []string, args []interface{}, key string) (bson.M, error) {
	predicate, err := db.ParsePredicate(where, args)
	if err != nil {
		return nil, err
	}
	return c.compilePredicate(predicate, key, key)
}
