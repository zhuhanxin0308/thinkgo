package mongo

import (
	"errors"
	"sync"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
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

// TestMongoNullAndIsOperatorSemantics 验证 MongoDB 支持 IS/IS NOT nil 并拒绝与 nil 的顺序或模式比较 (#592)。
func TestMongoNullAndIsOperatorSemantics(t *testing.T) {
	connection := &MongoConnection{}
	base := db.NewDB(connection).Table("users")
	var typedNil *string

	for _, tc := range []struct {
		name   string
		query  *db.Query
		expect bson.M
	}{
		{"eq nil", base.Where("deleted_at", nil), bson.M{"deleted_at": nil}},
		{"IS nil", base.Where("deleted_at", "IS", typedNil), bson.M{"deleted_at": nil}},
		{"neq nil", base.Where("deleted_at", "!=", nil), bson.M{"deleted_at": bson.M{"$ne": nil}}},
		{"IS NOT nil", base.Where("deleted_at", "IS NOT", nil), bson.M{"deleted_at": bson.M{"$ne": nil}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			predicate, err := tc.query.Predicate()
			if err != nil {
				t.Fatalf("构造谓词失败: %v", err)
			}
			filter, err := connection.compilePredicate(predicate, "_id", "id")
			if err != nil {
				t.Fatalf("编译 MongoDB NULL 条件失败: %v", err)
			}
			if len(filter) != 1 {
				t.Fatalf("过滤结构不符: %#v", filter)
			}
			if tc.expect["deleted_at"] == nil {
				if val, exists := filter["deleted_at"]; !exists || val != nil {
					t.Fatalf("期望 deleted_at: nil，实际 %#v", filter)
				}
			} else {
				cond, ok := filter["deleted_at"].(bson.M)
				if !ok || cond["$ne"] != nil {
					t.Fatalf("期望 $ne: nil，实际 %#v", filter)
				}
			}
		})
	}

	for index, query := range []*db.Query{
		base.Where("age", ">", nil),
		base.Where("age", ">=", nil),
		base.Where("age", "<", nil),
		base.Where("age", "<=", nil),
		base.Where("name", "LIKE", nil),
		base.WhereBetween("age", nil, 10),
		base.WhereBetween("age", 1, nil),
		base.Where("age", "IS", 1),
		base.Where("age", "IS NOT", true),
	} {
		predicate, err := query.Predicate()
		if err != nil {
			t.Fatalf("case %d 构造谓词失败: %v", index, err)
		}
		if _, err := connection.compilePredicate(predicate, "_id", "id"); !errors.Is(err, db.ErrInvalidQuery) {
			t.Fatalf("case %d 应返回 ErrInvalidQuery，实际为 %v", index, err)
		}
	}
}
