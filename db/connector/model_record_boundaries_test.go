package connector

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type sqliteLifecycleRecord struct {
	*db.Model
	ID         int64  `thinkgo:"id"`
	Name       string `thinkgo:"name"`
	DeleteTime *int64 `thinkgo:"delete_time"`
}

// TestSQLiteRecordSaveFieldsPreservesRemainingChanges 验证分步保存只确认指定字段，其余修改仍可继续保存。
func TestSQLiteRecordSaveFieldsPreservesRemainingChanges(t *testing.T) {
	database := newSqliteTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	owner := &sqliteRecordUser{Name: "Ada", Status: 1}
	model := newSQLiteRecordModel(t, context.Background(), database, owner)
	if err := owner.Save(); err != nil {
		t.Fatal(err)
	}
	owner.Name, owner.Status = "Grace", 2
	if err := owner.SaveFields("name"); err != nil {
		t.Fatalf("保存指定字段失败: %v", err)
	}
	row, err := database.Table("users").Where("id", owner.ID).Find()
	if err != nil || row["name"] != "Grace" || row["status"] != int64(1) {
		t.Fatalf("分步保存越过了字段边界: row=%#v err=%v", row, err)
	}
	if err := owner.Save(); err != nil {
		t.Fatalf("保存剩余脏字段失败: %v", err)
	}
	row, err = database.Table("users").Where("id", owner.ID).Find()
	if err != nil || row["status"] != int64(2) {
		t.Fatalf("未保存字段的变更丢失: row=%#v err=%v", row, err)
	}
	var partial sqliteRecordUser
	if found, err := model.Field("id,name").Where("id", owner.ID).Find(&partial); err != nil || !found {
		t.Fatalf("部分字段查询失败: found=%v err=%v", found, err)
	}
	if err := partial.SaveFields("status"); err != nil {
		t.Fatalf("未加载字段应允许显式保存零值: %v", err)
	}
	row, err = database.Table("users").Where("id", owner.ID).Find()
	if err != nil || row["status"] != int64(0) || row["name"] != "Grace" {
		t.Fatalf("未加载字段的零值没有保存: row=%#v err=%v", row, err)
	}
}

// TestSQLiteRecordMissingRowPreservesPendingSave 验证外部删除不会使失败的保存悄悄清除待写字段。
func TestSQLiteRecordMissingRowPreservesPendingSave(t *testing.T) {
	database := newSqliteTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	owner := &sqliteRecordUser{Name: "Ada", Status: 1}
	model := newSQLiteRecordModel(t, context.Background(), database, owner)
	if err := owner.Save(); err != nil {
		t.Fatal(err)
	}
	var loaded sqliteRecordUser
	if found, err := model.Where("id", owner.ID).Find(&loaded); err != nil || !found {
		t.Fatalf("加载记录失败: found=%v err=%v", found, err)
	}
	if _, err := database.Table("users").Where("id", owner.ID).Delete(); err != nil {
		t.Fatal(err)
	}
	loaded.Name = "待重试"
	if err := loaded.Save(); !errors.Is(err, db.ErrModelRecordMissing) {
		t.Fatalf("外部删除后的保存必须报告记录缺失: %v", err)
	}
	// 恢复同一主键后重试，只有保留了原始快照差异才会执行真实更新。
	if _, err := database.Table("users").Insert(map[string]interface{}{"id": owner.ID, "name": "恢复数据", "status": 1}); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Save(); err != nil {
		t.Fatalf("恢复记录后的重试失败: %v", err)
	}
	row, err := database.Table("users").Where("id", owner.ID).Find()
	if err != nil || row["name"] != "待重试" {
		t.Fatalf("失败的保存提前清除了待写变更: row=%#v err=%v", row, err)
	}
}

// TestSQLiteRecordRetryDoesNotRepeatValueTransformation 验证失败重试不会将修改器结果再次作为输入。
func TestSQLiteRecordRetryDoesNotRepeatValueTransformation(t *testing.T) {
	database := newSqliteTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	owner := &sqliteRecordUser{Name: "Ada", Status: 1}
	model := newSQLiteRecordModel(t, context.Background(), database, owner)
	setterCalls := 0
	if err := model.Setter("name", func(value interface{}, _ map[string]interface{}) interface{} {
		setterCalls++
		return "stored:" + value.(string)
	}); err != nil {
		t.Fatal(err)
	}
	if err := model.Getter("name", func(value interface{}, _ map[string]interface{}) interface{} {
		return strings.TrimPrefix(value.(string), "stored:")
	}); err != nil {
		t.Fatal(err)
	}
	if err := owner.Save(); err != nil {
		t.Fatal(err)
	}
	var loaded sqliteRecordUser
	if found, err := model.Where("id", owner.ID).Find(&loaded); err != nil || !found || loaded.Name != "Ada" {
		t.Fatalf("获取器没有保持展示值: loaded=%#v found=%v err=%v", loaded, found, err)
	}
	if err := loaded.Save(); err != nil || setterCalls != 1 {
		t.Fatalf("未修改的展示值不应重复执行修改器: calls=%d err=%v", setterCalls, err)
	}
	if _, err := database.Execute("CREATE TRIGGER block_model_update BEFORE UPDATE ON users BEGIN SELECT RAISE(ABORT, 'temporarily blocked'); END"); err != nil {
		t.Fatal(err)
	}
	loaded.Name = "Grace"
	if err := loaded.Save(); err == nil {
		t.Fatal("触发器拒绝的更新必须失败")
	}
	row, err := database.Table("users").Where("id", owner.ID).Find()
	if err != nil || row["name"] != "stored:Ada" || loaded.Name != "Grace" {
		t.Fatalf("失败写入改变了数据库或输入字段: row=%#v value=%q err=%v", row, loaded.Name, err)
	}
	if _, err := database.Execute("DROP TRIGGER block_model_update"); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Save(); err != nil {
		t.Fatalf("解除暂时故障后的保存重试失败: %v", err)
	}
	if err := loaded.Save(); err != nil || setterCalls != 3 {
		t.Fatalf("成功后的无变化保存不应重新转换字段: calls=%d err=%v", setterCalls, err)
	}
	row, err = database.Table("users").Where("id", owner.ID).Find()
	if err != nil || row["name"] != "stored:Grace" {
		t.Fatalf("修改器转换被重复叠加或变更被丢弃: row=%#v err=%v", row, err)
	}
}

// TestSQLiteRecordSoftDeleteLifecycle 验证软删除、恢复和物理删除始终针对同一记录，并同步删除字段。
func TestSQLiteRecordSoftDeleteLifecycle(t *testing.T) {
	database := newSqliteTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Execute("CREATE TABLE lifecycle_records (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, delete_time INTEGER)"); err != nil {
		t.Fatal(err)
	}
	owner := &sqliteLifecycleRecord{Name: "Ada"}
	model, err := db.NewModelFor(context.Background(), database, owner)
	if err != nil {
		t.Fatal(err)
	}
	model.Table("lifecycle_records").AutoTimestamp(false).SoftDelete()
	if err := owner.Save(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Delete(); err != nil {
		t.Fatalf("记录软删除失败: %v", err)
	}
	if count, err := model.Count(); err != nil || count != 0 {
		t.Fatalf("默认查询仍包含软删除记录: count=%d err=%v", count, err)
	}
	if count, err := model.WithTrashed().Count(); err != nil || count != 1 {
		t.Fatalf("软删除不应物理移除记录: count=%d err=%v", count, err)
	}
	if owner.DeleteTime == nil || *owner.DeleteTime == 0 {
		t.Error("成功软删除后应同步记录的删除时间")
	}
	var trashed sqliteLifecycleRecord
	if found, err := model.WithTrashed().Where("id", owner.ID).Find(&trashed); err != nil || !found || trashed.DeleteTime == nil {
		t.Fatalf("查询软删除记录失败: record=%#v found=%v err=%v", trashed, found, err)
	}
	owner.Name = "恢复后的修改"
	if err := owner.Save(); !errors.Is(err, db.ErrModelRecordMissing) {
		t.Fatalf("已软删除记录应先恢复再保存: %v", err)
	}
	trashed.Name = owner.Name
	if err := trashed.Restore(); err != nil {
		t.Fatalf("恢复软删除记录失败: %v", err)
	}
	if trashed.DeleteTime != nil {
		t.Error("成功恢复后应清空记录的删除时间")
	}
	if err := trashed.Save(); err != nil {
		t.Fatalf("恢复后的记录不能继续保存: %v", err)
	}
	row, err := database.Table("lifecycle_records").Where("id", owner.ID).Find()
	if err != nil || row["name"] != "恢复后的修改" || row["delete_time"] != nil {
		t.Fatalf("恢复生命周期状态不一致: row=%#v err=%v", row, err)
	}
	if err := trashed.Delete(); err != nil {
		t.Fatal(err)
	}
	if err := trashed.ForceDelete(); err != nil {
		t.Fatalf("强制删除软删除记录失败: %v", err)
	}
	if count, err := model.WithTrashed().Count(); err != nil || count != 0 {
		t.Fatalf("强制删除仍留下记录: count=%d err=%v", count, err)
	}
	if err := trashed.Restore(); !errors.Is(err, db.ErrModelRecordMissing) {
		t.Fatalf("物理删除后不得恢复不存在的记录: %v", err)
	}
}

// TestSQLiteRecordEndedTransactionSkipsCallbacks 验证事务结束后拒绝执行所有模型写回调。
func TestSQLiteRecordEndedTransactionSkipsCallbacks(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "回滚"
		if commit {
			name = "提交"
		}
		t.Run(name, func(t *testing.T) {
			database := newSqliteTestDB(t)
			t.Cleanup(func() { _ = database.Close() })
			transaction, err := database.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = transaction.Rollback() })
			model := db.NewModel(database, "users").WithTx(transaction)
			callbackCalls := 0
			for _, event := range []db.ModelEventType{db.ModelBeforeInsert, db.ModelBeforeUpdate, db.ModelBeforeDelete} {
				if err := model.On(event, func(map[string]interface{}) bool { callbackCalls++; return true }); err != nil {
					t.Fatal(err)
				}
			}
			if err := model.Setter("name", func(value interface{}, _ map[string]interface{}) interface{} { callbackCalls++; return value }); err != nil {
				t.Fatal(err)
			}
			prepared := model.Where("id", 1)
			if commit {
				err = transaction.Commit()
			} else {
				err = transaction.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := model.Create(&sqliteRecordUser{Name: "拒绝创建"}); !errors.Is(err, db.ErrTransactionDone) {
				t.Fatalf("事务结束后显式创建错误: %v", err)
			}
			if _, err := prepared.Update(map[string]interface{}{"name": "拒绝更新"}); !errors.Is(err, db.ErrTransactionDone) {
				t.Fatalf("事务结束后旧查询更新错误: %v", err)
			}
			if _, err := prepared.Delete(); !errors.Is(err, db.ErrTransactionDone) {
				t.Fatalf("事务结束后旧查询删除错误: %v", err)
			}
			if _, err := prepared.Insert(map[string]interface{}{"name": "拒绝插入"}); !errors.Is(err, db.ErrTransactionDone) {
				t.Fatalf("事务结束后旧查询插入错误: %v", err)
			}
			if callbackCalls != 0 {
				t.Fatalf("已结束事务仍执行了 %d 次模型回调", callbackCalls)
			}
		})
	}
}

// TestSQLiteRecordRefreshAfterTransactionRollback 验证事务回滚后可重新读取数据库状态，再继续保存同一记录。
func TestSQLiteRecordRefreshAfterTransactionRollback(t *testing.T) {
	database := newSqliteTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	owner := &sqliteRecordUser{Name: "原始值", Status: 1}
	model := newSQLiteRecordModel(t, context.Background(), database, owner)
	if err := owner.Save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelRecordOperationTimeout)
	defer cancel()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transaction.Rollback() })
	transactionModel := model.WithTx(transaction).WithContext(ctx)
	owner.Name = "被回滚的值"
	if err := transactionModel.Save(); err != nil {
		t.Fatalf("事务派生模型保存失败: %v", err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := transactionModel.Refresh(); !errors.Is(err, db.ErrTransactionDone) {
		t.Fatalf("已结束事务不得通过 Refresh 恢复执行: %v", err)
	}
	if err := owner.Save(); !errors.Is(err, db.ErrModelRecordTransaction) {
		t.Fatalf("回滚后的共享记录状态应要求重新读取: %v", err)
	}
	if err := owner.Refresh(); err != nil || owner.Name != "原始值" {
		t.Fatalf("非事务句柄应重新加载回滚后的状态: owner=%#v err=%v", owner, err)
	}
	if err := model.Save(); !errors.Is(err, db.ErrModelRecordStale) {
		t.Fatalf("Refresh 后旧的基础模型句柄不能继续保存: %v", err)
	}
	owner.Name = "重新保存"
	if err := owner.Save(); err != nil {
		t.Fatalf("Refresh 后记录未重新建立保存快照: %v", err)
	}
	row, err := database.Table("users").Where("id", owner.ID).Find()
	if err != nil || row["name"] != "重新保存" {
		t.Fatalf("Refresh 后保存丢失了变更: row=%#v err=%v", row, err)
	}
}

// TestSQLiteRecordRefreshLoadsMissingColumns 验证刷新会重新加载完整记录并重建字段可写状态。
func TestSQLiteRecordRefreshLoadsMissingColumns(t *testing.T) {
	database := newSqliteTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	owner := &sqliteRecordUser{Name: "Ada", Status: 8}
	model := newSQLiteRecordModel(t, context.Background(), database, owner)
	if err := owner.Save(); err != nil {
		t.Fatal(err)
	}
	var partial sqliteRecordUser
	if found, err := model.Field("id,name").Where("id", owner.ID).Find(&partial); err != nil || !found {
		t.Fatalf("部分字段查询失败: found=%v err=%v", found, err)
	}
	partial.Name = "放弃的修改"
	if err := partial.Refresh(); err != nil || partial.Name != "Ada" || partial.Status != 8 {
		t.Fatalf("刷新未重新读取完整记录: record=%#v err=%v", partial, err)
	}
	partial.Status = 0
	if err := partial.Save(); err != nil {
		t.Fatalf("刷新后新加载字段不能隐式保存: %v", err)
	}
	row, err := database.Table("users").Where("id", owner.ID).Find()
	if err != nil || row["status"] != int64(0) || row["name"] != "Ada" {
		t.Fatalf("刷新后的字段状态错误: row=%#v err=%v", row, err)
	}
}

// TestSQLiteRecordFailedRefreshPreservesOwner 验证刷新缺失记录时不会清空调用方数据和记录身份。
func TestSQLiteRecordFailedRefreshPreservesOwner(t *testing.T) {
	database := newSqliteTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	owner := &sqliteRecordUser{Name: "Ada", Status: 1}
	model := newSQLiteRecordModel(t, context.Background(), database, owner)
	if err := owner.Save(); err != nil {
		t.Fatal(err)
	}
	originalKey := owner.ID
	if _, err := database.Table("users").Where("id", originalKey).Delete(); err != nil {
		t.Fatal(err)
	}
	owner.Name = "尚未保存"
	if err := owner.Refresh(); !errors.Is(err, db.ErrModelRecordMissing) {
		t.Fatalf("刷新已删除记录必须报告缺失: %v", err)
	}
	if owner.ID != originalKey || owner.Name != "尚未保存" || owner.Model != model {
		t.Fatalf("失败的刷新破坏了调用方记录: owner=%#v", owner)
	}
}
