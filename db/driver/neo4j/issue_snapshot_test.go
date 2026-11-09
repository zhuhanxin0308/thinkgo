package neo4j

import (
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

// TestNeo4jRejectsInvalidSnapshotBeforeConnection 验证直接驱动入口不会丢失请求构造阶段的参数错误。
func TestNeo4jRejectsInvalidSnapshotBeforeConnection(t *testing.T) {
	data := map[string]interface{}{}
	data["self"] = data
	connection := &Neo4jConnection{}
	if _, err := connection.Insert(t.Context(), db.NewInsertRequest("items", data, "id", true)); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("插入参数错误被连接或主键错误覆盖: %v", err)
	}
	if _, err := connection.Update(t.Context(), db.NewUpdateRequest("items", data, Predicate{}, "id")); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("更新参数错误被连接错误覆盖: %v", err)
	}
}
