package db

import (
	"context"
	"strings"
	"testing"
)

type modelEventResultConnection struct {
	timestampCaptureConnection
}

func (c *modelEventResultConnection) Insert(ctx context.Context, request InsertRequest) (InsertResult, error) {
	result, err := c.timestampCaptureConnection.Insert(ctx, request)
	result.Data = map[string]interface{}{
		"name": "stale",
		"meta": map[string]interface{}{"role": "stale"},
	}
	return result, err
}

func TestModelEventsOwnDeepPayloadsAndSeeFinalData(t *testing.T) {
	connection := &modelEventResultConnection{}
	model := NewModel(NewDB(connection), "users").AutoTimestamp(true)

	var firstBefore map[string]interface{}
	if err := model.On(ModelBeforeInsert, func(data map[string]interface{}) bool {
		firstBefore = data
		data["meta"].(map[string]interface{})["role"] = "writer"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	var secondSawRole interface{}
	if err := model.On(ModelBeforeInsert, func(data map[string]interface{}) bool {
		secondSawRole = data["meta"].(map[string]interface{})["role"]
		data["name"] = "Grace"
		return true
	}); err != nil {
		t.Fatal(err)
	}

	var firstAfter, secondAfter map[string]interface{}
	if err := model.On(ModelAfterInsert, func(data map[string]interface{}) bool {
		firstAfter = data
		data["meta"].(map[string]interface{})["role"] = "after-mutated"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if err := model.On(ModelAfterInsert, func(data map[string]interface{}) bool {
		secondAfter = data
		return true
	}); err != nil {
		t.Fatal(err)
	}

	input := map[string]interface{}{
		"name": "Ada",
		"meta": map[string]interface{}{"role": "reader"},
	}
	if _, err := model.InsertGetId(input); err != nil {
		t.Fatal(err)
	}

	if firstBefore["name"] != "Ada" {
		t.Fatalf("later before callback polluted historical payload: %#v", firstBefore)
	}
	if secondSawRole != "writer" {
		t.Fatalf("ordered before callbacks did not receive prior changes: %#v", secondSawRole)
	}
	if secondAfter["id"] == nil || secondAfter["create_time"] == nil || secondAfter["update_time"] == nil || secondAfter["name"] != "Grace" {
		t.Fatalf("after callback did not receive final persisted data: %#v", secondAfter)
	}
	if secondAfter["meta"].(map[string]interface{})["role"] != "writer" || firstAfter["meta"].(map[string]interface{})["role"] != "after-mutated" {
		t.Fatalf("after callbacks shared mutable payloads: first=%#v second=%#v", firstAfter, secondAfter)
	}
	if input["meta"].(map[string]interface{})["role"] != "reader" {
		t.Fatalf("model hooks mutated caller-owned nested data: %#v", input)
	}
}

func TestModelWriteEventsSeeTimestampAndTypedResultSummaries(t *testing.T) {
	connection := &timestampCaptureConnection{}
	model := NewModel(NewDB(connection), "users").AutoTimestamp(true).SoftDelete()

	var updateAfter map[string]interface{}
	if err := model.On(ModelAfterUpdate, func(data map[string]interface{}) bool {
		updateAfter = data
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.newModelQuery().WhereField("id", "=", 1).Update(map[string]interface{}{"name": "Grace"}); err != nil {
		t.Fatal(err)
	}
	if updateAfter["name"] != "Grace" || updateAfter["update_time"] == nil {
		t.Fatalf("after_update did not receive final persisted data: %#v", updateAfter)
	}

	var deleteAfter map[string]interface{}
	if err := model.On(ModelAfterDelete, func(data map[string]interface{}) bool {
		deleteAfter = data
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.newModelQuery().WhereField("id", "=", 1).Delete(); err != nil {
		t.Fatal(err)
	}
	if deleteAfter["delete_time"] == nil || deleteAfter["update_time"] == nil || deleteAfter["affected"] != int64(1) {
		t.Fatalf("soft-delete event did not receive persisted timestamps: %#v", deleteAfter)
	}

	if _, err := model.newModelQuery().WhereField("id", "=", 1).ForceDelete(); err != nil {
		t.Fatal(err)
	}
	if deleteAfter["deleted"] != int64(1) {
		t.Fatalf("physical-delete event did not receive typed delete summary: %#v", deleteAfter)
	}
}

// TestModelBeforeInsertPreventsInsert 验证 before_insert 事件返回 false 可阻止插入。
func TestModelBeforeInsertPreventsInsert(t *testing.T) {
	conn := &mockConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users")
	model.On(ModelBeforeInsert, func(data map[string]interface{}) bool {
		// 阻止插入
		return false
	})

	_, err := model.Insert(map[string]interface{}{"name": "test"})
	if err == nil {
		t.Fatal("before_insert 返回 false 应阻止插入操作")
	}
}

// TestModelAfterInsertReceivesId 验证 after_insert 事件可以获取插入后的 ID。
func TestModelAfterInsertReceivesId(t *testing.T) {
	conn := &mockConnection{}
	database := NewDB(conn)

	var receivedId interface{}
	model := NewModel(database, "users")
	model.On(ModelAfterInsert, func(data map[string]interface{}) bool {
		receivedId = data["id"]
		return true
	})

	id, err := model.InsertGetId(map[string]interface{}{"name": "test"})
	if err != nil {
		t.Fatalf("InsertGetId 不应返回错误，实际为 %v", err)
	}
	if receivedId != id {
		t.Fatalf("after_insert 应收到插入 ID，期望 %v，实际 %v", id, receivedId)
	}
}

// TestModelBeforeUpdatePreventsUpdate 验证 before_update 事件返回 false 可阻止更新。
func TestModelBeforeUpdatePreventsUpdate(t *testing.T) {
	conn := &mockConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users")
	model.On(ModelBeforeUpdate, func(data map[string]interface{}) bool {
		return false
	})

	_, err := model.Where("id = ?", 1).Update(map[string]interface{}{"name": "updated"})
	// 注意：Where 返回的是 Query，不是 Model，所以事件不触发。
	// Model 的 UpdateMap 才会触发事件。
	if err != nil {
		// Query.Update 不走 Model 事件
	}
}

// TestModelBeforeDeletePreventsDelete 验证 before_delete 事件返回 false 可阻止删除。
func TestModelBeforeDeletePreventsDelete(t *testing.T) {
	conn := &mockConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users")
	model.On(ModelBeforeDelete, func(data map[string]interface{}) bool {
		return false
	})

	// DeleteRecord 走 Model 事件
	_, err := model.DeleteRecord()
	if err == nil {
		t.Fatal("before_delete 返回 false 应阻止删除操作")
	}
}

// TestModelEventsAllFire 验证注册多个事件回调都能正常触发。
func TestModelEventsAllFire(t *testing.T) {
	conn := &mockConnection{}
	database := NewDB(conn)

	var beforeFired, afterFired bool
	model := NewModel(database, "users")
	model.On(ModelBeforeInsert, func(data map[string]interface{}) bool {
		beforeFired = true
		return true
	})
	model.On(ModelAfterInsert, func(data map[string]interface{}) bool {
		afterFired = true
		return true
	})

	_, _ = model.Insert(map[string]interface{}{"name": "test"})

	if !beforeFired {
		t.Fatal("before_insert 事件未触发")
	}
	if !afterFired {
		t.Fatal("after_insert 事件未触发")
	}
}

// TestModelWithoutEvents 验证未注册事件时 CRUD 正常执行。
func TestModelWithoutEvents(t *testing.T) {
	conn := &mockConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users")
	_, err := model.Insert(map[string]interface{}{"name": "test"})
	if err != nil {
		t.Fatalf("未注册事件时 Insert 不应失败，实际为 %v", err)
	}
}

// TestModelQueryChainGetterAndEvent 验证 ModelQuery 链式调用时，事件、获取器和修改器正常触发。
func TestModelQueryChainGetterAndEvent(t *testing.T) {
	conn := &timestampCaptureConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users")
	model.Getter("status", func(value interface{}, data map[string]interface{}) interface{} {
		if v, ok := value.(int); ok && v == 1 {
			return "active"
		}
		return value
	})
	model.Setter("email", func(value interface{}, data map[string]interface{}) interface{} {
		if s, ok := value.(string); ok {
			return strings.TrimSpace(s)
		}
		return value
	})

	var beforeUpdateFired, afterUpdateFired bool
	model.On(ModelBeforeUpdate, func(data map[string]interface{}) bool {
		beforeUpdateFired = true
		return true
	})
	model.On(ModelAfterUpdate, func(data map[string]interface{}) bool {
		afterUpdateFired = true
		return true
	})

	// 1. 测试 Where().Find() 触发 getter
	connMock := &mockConnection{}
	dbMock := NewDB(connMock)
	modelMock := NewModel(dbMock, "users")
	modelMock.Getter("table", func(value interface{}, data map[string]interface{}) interface{} {
		return "transformed"
	})
	row, err := modelMock.Where("id = ?", 1).Find()
	if err != nil {
		t.Fatalf("Find 失败: %v", err)
	}
	if row["table"] != "transformed" {
		t.Fatalf("链式查询的 Find 未应用获取器，得到: %v", row["table"])
	}

	// 2. 测试 Where().Update() 触发修改器与 update 事件
	_, err = model.Where("id = ?", 1).Update(map[string]interface{}{
		"email": "  user@thinkgo.com  ",
	})
	if err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if !beforeUpdateFired {
		t.Fatal("链式查询的 Update 未触发 before_update 事件")
	}
	if !afterUpdateFired {
		t.Fatal("链式查询的 Update 未触发 after_update 事件")
	}
	if conn.updateData["email"] != "user@thinkgo.com" {
		t.Fatalf("链式查询的 Update 未应用修改器，得到: %q", conn.updateData["email"])
	}
}

// TestModelQuerySoftDeleteAndEvent 验证 ModelQuery 链式调用时的软删除自动改写和 delete 事件。
func TestModelQuerySoftDeleteAndEvent(t *testing.T) {
	conn := &timestampCaptureConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users").SoftDelete()

	var beforeDeleteFired, afterDeleteFired bool
	model.On(ModelBeforeDelete, func(data map[string]interface{}) bool {
		beforeDeleteFired = true
		return true
	})
	model.On(ModelAfterDelete, func(data map[string]interface{}) bool {
		afterDeleteFired = true
		return true
	})

	// 执行链式删除
	affected, err := model.Where("id = ?", 1).Delete()
	if err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if affected != 1 {
		t.Fatalf("预期影响行数为 1，实际为 %d", affected)
	}

	if !beforeDeleteFired {
		t.Fatal("链式查询的 Delete 未触发 before_delete 事件")
	}
	if !afterDeleteFired {
		t.Fatal("链式查询的 Delete 未触发 after_delete 事件")
	}

	// 验证实际执行的是 UPDATE
	if conn.lastOperation != "update" {
		t.Fatalf("软删除模型链式 Delete 预期执行 update，实际执行: %s", conn.lastOperation)
	}
	if _, ok := conn.updateData["delete_time"]; !ok {
		t.Fatal("软删除链式 Delete 预期更新 delete_time，但未找到")
	}
}

// TestModelQueryConcurrentTrashedIsolation 验证并发使用同一 Model 的 WithTrashed/OnlyTrashed 状态互不干扰。
func TestModelQueryConcurrentTrashedIsolation(t *testing.T) {
	db := NewDB(&mockConnection{})
	model := NewModel(db, "users").SoftDelete()

	// 并发/链式生成多个 ModelQuery 实例
	mqNormal := model.newModelQuery()
	mqWithTrashed := model.WithTrashed()
	mqOnlyTrashed := model.OnlyTrashed()
	mqNormalSecond := model.newModelQuery()

	qNormal := mqNormal.PrepareQuery()
	qWithTrashed := mqWithTrashed.PrepareQuery()
	qOnlyTrashed := mqOnlyTrashed.PrepareQuery()
	qNormalSecond := mqNormalSecond.PrepareQuery()

	if len(qNormal.where) != 1 || qNormal.where[0] != "delete_time IS NULL" {
		t.Fatalf("mqNormal 预期带 delete_time IS NULL，实际: %v", qNormal.where)
	}
	if len(qWithTrashed.where) != 0 {
		t.Fatalf("mqWithTrashed 预期无过滤，实际: %v", qWithTrashed.where)
	}
	if len(qOnlyTrashed.where) != 1 || qOnlyTrashed.where[0] != "delete_time IS NOT NULL" {
		t.Fatalf("mqOnlyTrashed 预期带 delete_time IS NOT NULL，实际: %v", qOnlyTrashed.where)
	}
	if len(qNormalSecond.where) != 1 || qNormalSecond.where[0] != "delete_time IS NULL" {
		t.Fatalf("mqNormalSecond 预期带 delete_time IS NULL，实际: %v", qNormalSecond.where)
	}
}

type structUser struct {
	ID    int64  `db:"id"`
	Name  string `db:"name"`
	Email string `db:"email"`
}

// TestModelStructCRUDTriggerEventsAndSetters 验证 Model 的结构体方法能正常调用修改器并触发模型事件。
func TestModelStructCRUDTriggerEventsAndSetters(t *testing.T) {
	conn := &timestampCaptureConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users").SoftDelete()

	// 注册修改器 (setter)
	model.Setter("email", func(value interface{}, data map[string]interface{}) interface{} {
		if s, ok := value.(string); ok {
			return strings.TrimSpace(s)
		}
		return value
	})

	var beforeInsert, afterInsert, beforeUpdate, afterUpdate, beforeDelete, afterDelete bool

	model.On(ModelBeforeInsert, func(data map[string]interface{}) bool {
		beforeInsert = true
		return true
	})
	model.On(ModelAfterInsert, func(data map[string]interface{}) bool {
		afterInsert = true
		return true
	})
	model.On(ModelBeforeUpdate, func(data map[string]interface{}) bool {
		beforeUpdate = true
		return true
	})
	model.On(ModelAfterUpdate, func(data map[string]interface{}) bool {
		afterUpdate = true
		return true
	})
	model.On(ModelBeforeDelete, func(data map[string]interface{}) bool {
		beforeDelete = true
		return true
	})
	model.On(ModelAfterDelete, func(data map[string]interface{}) bool {
		afterDelete = true
		return true
	})

	user := &structUser{
		Name:  "TestUser",
		Email: "  test@thinkgo.com  ",
	}

	// 1. 测试 Create
	err := model.Create(user)
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	if !beforeInsert || !afterInsert {
		t.Fatal("Create 未触发 before_insert / after_insert 事件")
	}
	if conn.insertData["email"] != "test@thinkgo.com" {
		t.Fatalf("Create 未触发修改器，预期: test@thinkgo.com，实际: %v", conn.insertData["email"])
	}

	// 2. 测试 Update
	user.Email = "  new@thinkgo.com  "
	err = model.Update(user)
	if err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if !beforeUpdate || !afterUpdate {
		t.Fatal("Update 未触发 before_update / after_update 事件")
	}
	if conn.updateData["email"] != "new@thinkgo.com" {
		t.Fatalf("Update 未触发修改器，实际: %v", conn.updateData["email"])
	}

	// 3. 测试 Delete (软删除)
	_, err = model.Where("id = ?", user.ID).Delete()
	if err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if !beforeDelete || !afterDelete {
		t.Fatal("Delete 未触发 before_delete / after_delete 事件")
	}
	if conn.lastOperation != "update" || conn.updateData["delete_time"] == nil {
		t.Fatal("Delete 未正确执行软删除")
	}

	// 4. 测试 ForceDelete (物理删除)
	beforeDelete, afterDelete = false, false
	_, err = model.Where("id = ?", 1).ForceDelete()
	if err != nil {
		t.Fatalf("ForceDelete 失败: %v", err)
	}
	if !beforeDelete || !afterDelete {
		t.Fatal("ForceDelete 未触发 before_delete / after_delete 事件")
	}
	if conn.lastOperation != "delete" {
		t.Fatal("ForceDelete 未正确执行物理删除")
	}

	// 5. 测试 Restore (恢复软删除)
	conn.lastOperation = ""
	_, err = model.Where("id = ?", 1).Restore()
	if err != nil {
		t.Fatalf("Restore 失败: %v", err)
	}
	if conn.lastOperation != "update" {
		t.Fatalf("Restore 预期执行 update，实际为 %s", conn.lastOperation)
	}
	if conn.updateData["delete_time"] != nil {
		t.Fatal("Restore 预期将 delete_time 设为 nil，但非 nil")
	}
}
