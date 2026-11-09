package mongo

import (
	"errors"
	"testing"
)

// TestMongoRejectsInvalidSnapshotBeforeConnection 验证直接驱动入口也在连接校验之前拒绝非法参数。
func TestMongoRejectsInvalidSnapshotBeforeConnection(t *testing.T) {
	data := map[string]interface{}{}
	data["self"] = data
	connection := &MongoConnection{}
	if _, err := connection.Insert(t.Context(), newInsertRequest("items", data, "id", false)); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("插入参数错误被连接错误覆盖: %v", err)
	}
	if _, err := connection.Update(t.Context(), newUpdateRequest("items", data, newPredicate(), "id")); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("更新参数错误被连接错误覆盖: %v", err)
	}
}
