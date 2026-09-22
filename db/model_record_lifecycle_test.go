package db

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type lifecycleRecord struct {
	*Model
	ID         int64
	Name       string
	Email      string `thinkgo:",omitempty"`
	CreateTime int64
	UpdateTime int64
	DeleteTime *int64
}

type recordLifecycleConnection struct {
	capturingConnection
	rows         []map[string]any
	updateErr    error
	updateResult *UpdateResult
	count        int64
	insertID     any
	insertCalls  int
}

func (c *recordLifecycleConnection) Select(context.Context, SelectRequest) ([]map[string]any, error) {
	result := make([]map[string]any, len(c.rows))
	for i, row := range c.rows {
		result[i] = cloneDatabaseMap(row)
	}
	return result, nil
}

func (c *recordLifecycleConnection) Insert(ctx context.Context, request InsertRequest) (InsertResult, error) {
	c.insertCalls++
	result, err := c.capturingConnection.Insert(ctx, request)
	if c.insertID != nil {
		result.ID = c.insertID
	}
	return result, err
}

func (c *recordLifecycleConnection) Update(ctx context.Context, request UpdateRequest) (UpdateResult, error) {
	result, err := c.capturingConnection.Update(ctx, request)
	if c.updateErr != nil {
		return UpdateResult{}, c.updateErr
	}
	if c.updateResult != nil {
		result = *c.updateResult
		result.Data = request.Data()
	}
	return result, err
}

func (c *recordLifecycleConnection) Count(context.Context, CountRequest) (int64, error) {
	return c.count, nil
}

func loadLifecycleRecord(t *testing.T) (*lifecycleRecord, *recordLifecycleConnection) {
	t.Helper()
	c := &recordLifecycleConnection{rows: []map[string]any{{"id": int64(7), "name": "first", "email": "a"}}, count: 1}
	m := NewModel(NewDB(c), "records")
	var record lifecycleRecord
	if found, err := m.Find(&record); err != nil || !found {
		t.Fatalf("加载记录失败: %v", err)
	}
	return &record, c
}

func TestRecordPartialSaveRetryAndCallbackReentry(t *testing.T) {
	r, c := loadLifecycleRecord(t)
	r.Name, r.Email = "next", ""
	if err := r.SaveFields("name"); err != nil {
		t.Fatal(err)
	}
	if len(c.lastData) != 1 || c.lastData["name"] != "next" {
		t.Fatalf("部分保存错误: %#v", c.lastData)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if len(c.lastData) != 1 || c.lastData["email"] != "" {
		t.Fatalf("剩余零值修改丢失: %#v", c.lastData)
	}
	failure := errors.New("数据库暂时不可写")
	r.Name = "retry"
	c.updateErr = failure
	if err := r.Save(); !errors.Is(err, failure) {
		t.Fatalf("丢失数据库错误: %v", err)
	}
	c.updateErr = nil
	var nested error
	if err := r.On(ModelBeforeUpdate, func(map[string]any) bool { nested = r.Save(); return true }); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(nested, ErrModelRecordBusy) {
		t.Fatalf("写回调重入应拒绝: %v", nested)
	}
	if c.lastData["name"] != "retry" {
		t.Fatalf("失败后脏字段被清除: %#v", c.lastData)
	}
}

func TestRecordMissingAndUnchangedMatch(t *testing.T) {
	r, c := loadLifecycleRecord(t)
	r.Name = "next"
	c.updateResult = &UpdateResult{MatchedKnown: true, Matched: 0}
	if err := r.Save(); !errors.Is(err, ErrModelRecordMissing) {
		t.Fatalf("未匹配记录被当成保存成功: %v", err)
	}
	c.updateResult = &UpdateResult{}
	c.count = 0
	if err := r.Save(); !errors.Is(err, ErrModelRecordMissing) {
		t.Fatalf("影响0行且记录不存在被忽略: %v", err)
	}
	c.count = 1
	if err := r.Save(); err != nil {
		t.Fatalf("数据库匹配但值未变化不应失败: %v", err)
	}
	c.lastData = nil
	if err := r.Save(); err != nil || c.lastData != nil {
		t.Fatalf("稳定状态重复写入: %v", err)
	}
}

func TestRecordNewCreateTimestampsAndExplicitKey(t *testing.T) {
	for _, initialID := range []int64{0, 42} {
		r := &lifecycleRecord{ID: initialID, Name: "new"}
		c := &recordLifecycleConnection{}
		m, err := NewModelFor(context.Background(), NewDB(c), r)
		if err != nil {
			t.Fatal(err)
		}
		m.AutoTimestamp(true)
		if err := m.Create(r); err != nil {
			t.Fatal(err)
		}
		if r.ID == 0 || r.CreateTime == 0 || r.UpdateTime == 0 {
			t.Fatalf("创建后字段未回填: %+v", r)
		}
		if initialID != 0 && r.ID != initialID {
			t.Fatal("显式主键被替换")
		}
		if err := r.Save(); err != nil || c.insertCalls != 1 {
			t.Fatalf("Create后Save重复插入: %d %v", c.insertCalls, err)
		}
		if err := m.Create(r); !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("重复Create未拒绝: %v", err)
		}
		if _, err := NewModelFor(context.Background(), m.db, r); !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("重复绑定覆盖记录状态: %v", err)
		}
	}
}

func TestRecordPartialWritePreventsDuplicateInsert(t *testing.T) {
	type tinyRecord struct {
		*Model
		ID   uint8
		Name string
	}
	r := &tinyRecord{Name: "overflow"}
	c := &recordLifecycleConnection{insertID: int64(300)}
	if _, err := NewModelFor(context.Background(), NewDB(c), r); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); !errors.Is(err, ErrPartialWrite) {
		t.Fatalf("已落库ID溢出错误不明确: %v", err)
	}
	if err := r.Save(); !errors.Is(err, ErrPartialWrite) || c.insertCalls != 1 {
		t.Fatalf("部分写入发生重放: %d %v", c.insertCalls, err)
	}
}

func TestRecordProjectionRefreshAndIdentityGuards(t *testing.T) {
	r, c := loadLifecycleRecord(t)
	c.rows = []map[string]any{{"id": int64(7), "name": "partial"}}
	if found, err := r.Model.Find(r); err != nil || !found {
		t.Fatal(err)
	}
	r.Email = "unselected"
	if err := r.Save(); !errors.Is(err, ErrModelFieldNotLoaded) {
		t.Fatalf("未加载列被隐式写入: %v", err)
	}
	if err := r.SaveFields("email"); err != nil {
		t.Fatal(err)
	}
	for _, fields := range [][]string{nil, {"id"}, {"missing"}} {
		if err := r.SaveFields(fields...); !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("非法字段未拒绝: %v", err)
		}
	}
	old := r.Model
	r.ID = 9
	if err := r.Delete(); !errors.Is(err, ErrModelIdentityChanged) {
		t.Fatalf("改主键后仍删除: %v", err)
	}
	c.rows = []map[string]any{{"id": int64(7), "name": "refreshed", "email": "stored"}}
	if err := r.Refresh(); err != nil {
		t.Fatal(err)
	}
	if r.ID != 7 || r.Name != "refreshed" || r.Email != "stored" {
		t.Fatalf("Refresh未恢复数据库状态: %+v", r)
	}
	if err := old.Save(); !errors.Is(err, ErrModelRecordStale) {
		t.Fatalf("旧句柄未拒绝: %v", err)
	}
	c.rows = nil
	if err := r.Refresh(); !errors.Is(err, ErrModelRecordMissing) {
		t.Fatalf("刷新不存在记录未报错: %v", err)
	}
	if err := r.SetDB(NewDB(c)); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("已加载记录被换库: %v", err)
	}
	r.Table("other")
	if err := r.Save(); !errors.Is(err, ErrModelIdentityChanged) {
		t.Fatalf("改表后仍保存: %v", err)
	}
}

func TestRecordSnapshotMutableAndInvalidValues(t *testing.T) {
	type mutable struct {
		Values  map[string][]int
		Pointer *int
		Items   [2]string
	}
	number := 1
	value := mutable{Values: map[string][]int{"k": {1}}, Pointer: &number, Items: [2]string{"a", "b"}}
	before, err := snapshotRecordValue(reflect.ValueOf(value), make(map[recordSnapshotVisit]bool))
	if err != nil {
		t.Fatal(err)
	}
	value.Values["k"][0], number = 2, 2
	after, err := snapshotRecordValue(reflect.ValueOf(value), make(map[recordSnapshotVisit]bool))
	if err != nil || reflect.DeepEqual(before, after) {
		t.Fatalf("快照和当前可变值发生别名共享: %v", err)
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, invalid := range []any{cycle, make(chan int), func() {}, struct{ hidden []byte }{}, map[*int]string{&number: "k"}} {
		if _, err := snapshotRecordValue(reflect.ValueOf(invalid), make(map[recordSnapshotVisit]bool)); err == nil {
			t.Fatalf("不支持的快照值应失败: %T", invalid)
		}
	}
}

func TestRecordSoftDeleteRestoreSynchronizesOwner(t *testing.T) {
	r, c := loadLifecycleRecord(t)
	r.SoftDelete().AutoTimestamp(true)
	if err := r.Delete(); err != nil {
		t.Fatal(err)
	}
	if r.DeleteTime == nil || r.UpdateTime == 0 {
		t.Fatal("软删除状态未回填")
	}
	if err := r.Save(); !errors.Is(err, ErrModelRecordMissing) {
		t.Fatalf("删除记录允许保存: %v", err)
	}
	if err := r.Restore(); err != nil {
		t.Fatal(err)
	}
	if r.DeleteTime != nil {
		t.Fatal("恢复状态未回填")
	}
	c.lastData = nil
	if err := r.Save(); err != nil || c.lastData != nil {
		t.Fatalf("恢复后快照不一致: %v", err)
	}
	if err := r.ForceDelete(); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); !errors.Is(err, ErrModelRecordMissing) {
		t.Fatalf("物理删除后允许保存: %v", err)
	}
}

func TestRecordTransactionParticipationRequiresCommit(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	if _, err := raw.Exec("CREATE TABLE transaction_records (id INTEGER PRIMARY KEY, name TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("INSERT INTO transaction_records(id,name) VALUES(1,'first')"); err != nil {
		t.Fatal(err)
	}
	type transactionRecord struct {
		*Model
		ID   int64
		Name string
	}
	var record transactionRecord
	if found, err := NewModel(database, "transaction_records").Find(&record); err != nil || !found {
		t.Fatal(err)
	}
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	record.Name = "inside"
	bound := record.WithTx(tx)
	if err := bound.Save(); err != nil {
		t.Fatal(err)
	}
	if err := record.Save(); !errors.Is(err, ErrModelRecordTransaction) {
		t.Fatalf("未提交状态被非事务句柄接受: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	record.Name = "after"
	if err := record.Save(); err != nil {
		t.Fatalf("已提交状态不能继续保存: %v", err)
	}
	if err := bound.Save(); !errors.Is(err, ErrTransactionDone) {
		t.Fatalf("旧事务句柄仍然可写: %v", err)
	}
	name, err := database.Name("transaction_records").Where("id", record.ID).Value("name")
	if err != nil || name != "after" {
		t.Fatalf("提交后的修改未落库: %v %v", name, err)
	}
}

func TestRecordInsertUsesNormalizedBusinessIdentity(t *testing.T) {
	type keyedRecord struct {
		*Model
		Code string
		Name string
	}
	r := &keyedRecord{Code: "old", Name: "first"}
	c := &recordLifecycleConnection{}
	m, err := NewModelFor(context.Background(), NewDB(c), r)
	if err != nil {
		t.Fatal(err)
	}
	m.PrimaryKey("code")
	if err := m.Setter("code", func(any, map[string]any) any { return "normalized" }); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if r.Code != "normalized" {
		t.Fatalf("规范化后的主键未回填: %q", r.Code)
	}
	r.Name = "next"
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if len(c.lastArgs) != 1 || c.lastArgs[0] != "normalized" {
		t.Fatalf("更新使用了错误身份: %#v", c.lastArgs)
	}
}

func TestRecordAfterWriteChangesRemainDirty(t *testing.T) {
	r := &lifecycleRecord{Name: "inserted"}
	c := &recordLifecycleConnection{}
	if _, err := NewModelFor(context.Background(), NewDB(c), r); err != nil {
		t.Fatal(err)
	}
	if err := r.On(ModelAfterInsert, func(map[string]any) bool { r.Name = "after insert"; return true }); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if c.lastData["name"] != "inserted" {
		t.Fatal("插入事件应在写入后执行")
	}
	if err := r.On(ModelAfterUpdate, func(map[string]any) bool { r.Name = "after update"; return true }); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if c.lastData["name"] != "after insert" {
		t.Fatal("插入后回调修改丢失")
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if c.lastData["name"] != "after update" {
		t.Fatal("更新后回调修改丢失")
	}
}
