package mongo

import (
	"errors"
	"sync"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestMongoNativePredicateRejectsInvalidBusinessInputs 验证原生条件树的主键别名、嵌套条件和后端能力边界。
func TestMongoNativePredicateRejectsInvalidBusinessInputs(t *testing.T) {
	connection := &MongoConnection{}
	base := db.NewDB(connection).Table("users")
	id := bson.NewObjectID().Hex()
	queries := map[string]*db.Query{
		"alias_collision":   base.Where("id", id).Where("_id", id),
		"nested_invalid_id": base.Where("active", true).WhereOr("id", "invalid"),
		"invalid_id_list":   base.WhereIn("id", []any{id, "invalid"}),
		"invalid_pattern":   base.Where("name", "LIKE", 10),
		"column_expression": base.WhereColumn("name", "=", "alias"),
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			predicate, err := query.Predicate()
			if err != nil {
				t.Fatalf("构造合法通用条件失败: %v", err)
			}
			if _, err := connection.compilePredicate(predicate, "_id", "id"); !errors.Is(err, db.ErrInvalidQuery) {
				t.Fatalf("后端未拒绝不支持的输入: %v", err)
			}
		})
	}
}

// TestMongoEmptySetNeverMatches 验证空集合不会被降级成无条件查询。
func TestMongoEmptySetNeverMatches(t *testing.T) {
	connection := &MongoConnection{}
	predicate, err := db.NewDB(connection).Table("users").WhereIn("id", []any{}).Predicate()
	if err != nil {
		t.Fatal(err)
	}
	filter, err := connection.compilePredicate(predicate, "_id", "id")
	if err != nil || filter["$expr"] == nil {
		t.Fatalf("空集合失去了恒假语义: %#v %v", filter, err)
	}
}

// TestMongoConnectionClosesConcurrently 验证拆包后连接仍可由多个资源所有者幂等关闭。
func TestMongoConnectionClosesConcurrently(t *testing.T) {
	connection := newMongoMockConnection(t)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			if err := connection.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
}
