package db

import (
	"errors"
	"testing"
)

func TestOperationResultsExposeStableCounts(t *testing.T) {
	update := UpdateResult{
		Affected:      3,
		Matched:       3,
		Modified:      2,
		MatchedKnown:  true,
		ModifiedKnown: true,
	}
	if got := update.Count(); got != 2 {
		t.Fatalf("Update 简化数量应优先 Modified，实际 %d", got)
	}

	legacySQL := UpdateResult{Affected: 4}
	if got := legacySQL.Count(); got != 4 {
		t.Fatalf("未知 matched/modified 时应返回驱动 affected，实际 %d", got)
	}

	if err := (InsertResult{Affected: -1}).Validate(); !errors.Is(err, ErrInvalidOperationResult) {
		t.Fatalf("负计数应拒绝，实际 %v", err)
	}
	if err := (UpdateResult{Affected: 1, Matched: -1, MatchedKnown: true}).Validate(); !errors.Is(err, ErrInvalidOperationResult) {
		t.Fatalf("已知 matched 不能为负数，实际 %v", err)
	}
	if err := (DeleteResult{Deleted: -1}).Validate(); !errors.Is(err, ErrInvalidOperationResult) {
		t.Fatalf("删除数量不能为负数，实际 %v", err)
	}
}

func TestInsertResultRequiresRealID(t *testing.T) {
	_, err := (InsertResult{Affected: 1}).InsertedID()
	if !errors.Is(err, ErrInsertIDUnavailable) {
		t.Fatalf("缺少真实 ID 应返回 ErrInsertIDUnavailable，实际 %v", err)
	}

	result := InsertResult{Affected: 1, ID: "01JABC", IDKnown: true}
	id, err := result.InsertedID()
	if err != nil || id != "01JABC" {
		t.Fatalf("真实 ID 未保留类型和值: %#v, %v", id, err)
	}

	if err := (InsertResult{Affected: 1, IDKnown: true}).Validate(); !errors.Is(err, ErrInvalidOperationResult) {
		t.Fatalf("IDKnown 与 nil ID 矛盾时应拒绝，实际 %v", err)
	}
}

func TestPredicateSeparatesValidatedAndRawClauses(t *testing.T) {
	predicate := newPredicate().
		appendValidated("status = ?", []interface{}{"open"}).
		appendRaw("score > ?", []interface{}{10})

	clauses := predicate.Clauses()
	if clauses[0].UnsafeRaw || !clauses[1].UnsafeRaw {
		t.Fatalf("条件来源标记丢失: %#v", clauses)
	}
	clauses[0].Args[0] = "mutated"
	if got := predicate.Clauses()[0].Args[0]; got != "open" {
		t.Fatalf("Predicate 被外部修改: %v", got)
	}
}

func TestOperationRequestsCloneMutableInputs(t *testing.T) {
	nested := map[string]interface{}{"role": "admin"}
	bytes := []byte("secret")
	data := map[string]interface{}{"meta": nested, "payload": bytes}
	request := newInsertRequest("users", data, "id", true)

	nested["role"] = "changed"
	bytes[0] = 'X'
	first := request.Data()
	if got := first["meta"].(map[string]interface{})["role"]; got != "admin" {
		t.Fatalf("request 未深拷贝 nested map: %v", got)
	}
	if got := string(first["payload"].([]byte)); got != "secret" {
		t.Fatalf("request 未深拷贝 []byte: %q", got)
	}

	first["meta"].(map[string]interface{})["role"] = "external"
	if got := request.Data()["meta"].(map[string]interface{})["role"]; got != "admin" {
		t.Fatalf("Data accessor 暴露内部状态: %v", got)
	}
	if request.Table() != "users" || request.PrimaryKey() != "id" || !request.WantsID() {
		t.Fatalf("insert request metadata 不完整: %#v", request)
	}
}

func TestReadAndMutationRequestsCarryPrimaryKeyAndPredicate(t *testing.T) {
	predicate := newPredicate().appendValidated("id = ?", []interface{}{7})
	selectRequest := newSelectRequest("users", "name", predicate, "id", "id DESC", 10, 2, nil)
	updateRequest := newUpdateRequest("users", map[string]interface{}{"name": "Ada"}, predicate, "id")
	deleteRequest := newDeleteRequest("users", predicate, "id", true)
	countRequest := newCountRequest("users", predicate, "id")

	for name, primaryKey := range map[string]string{
		"select": selectRequest.PrimaryKey(),
		"update": updateRequest.PrimaryKey(),
		"delete": deleteRequest.PrimaryKey(),
		"count":  countRequest.PrimaryKey(),
	} {
		if primaryKey != "id" {
			t.Fatalf("%s request primary key=%q", name, primaryKey)
		}
	}
	if !deleteRequest.DetachRelations() {
		t.Fatal("delete request 丢失显式 detach 意图")
	}
	if got := updateRequest.Predicate().Clauses()[0].Args[0]; got != 7 {
		t.Fatalf("update predicate=%v", got)
	}

	aliased := newUpdateRequest("users", map[string]interface{}{"name": "Grace"}, predicate, "_id", "id")
	if aliased.PrimaryKey() != "_id" || aliased.ModelPrimaryKey() != "id" {
		t.Fatalf("storage/model primary-key mapping was lost: storage=%q model=%q", aliased.PrimaryKey(), aliased.ModelPrimaryKey())
	}
	if updateRequest.ModelPrimaryKey() != updateRequest.PrimaryKey() {
		t.Fatalf("ordinary requests must keep identical model/storage keys: storage=%q model=%q", updateRequest.PrimaryKey(), updateRequest.ModelPrimaryKey())
	}
}
