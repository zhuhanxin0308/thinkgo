//go:build cgo

package connector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type milliRecord struct {
	*db.Model
	ID        int64
	Name      string
	CreatedAt int64
	UpdatedAt int64
	DeletedAt *int64
}

// ConfigureModel 通过模型配置钩子继承全局毫秒模式及软删除行为。
func (*milliRecord) ConfigureModel(m *db.Model) error {
	m.Table("milli_records").SoftDelete("deleted_at")
	return nil
}

// TestUnixMilliSQLLifecycle 用真实 SQL 引擎验证模型、批量写入、事务提交及回滚。
func TestUnixMilliSQLLifecycle(t *testing.T) {
	RegisterBuiltins()
	database, err := db.Connect(db.Config{
		Type: "sqlite", Database: ":memory:", AutoTimestamp: true,
		TimestampValueType: db.TimestampValueTypeUnixMilli,
		CreateTimeField:    "created_at", UpdateTimeField: "updated_at",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := database.Execute("CREATE TABLE milli_records (id INTEGER PRIMARY KEY, name TEXT, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL, deleted_at BIGINT)"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before := time.Now().UnixMilli()
	record := &milliRecord{Name: "模型"}
	if _, err := db.NewModelFor(ctx, database, record); err != nil {
		t.Fatal(err)
	}
	if err := record.Save(); err != nil {
		t.Fatal(err)
	}
	check := func(id int64) *milliRecord {
		t.Helper()
		var found milliRecord
		ok, err := record.WithTrashed().Where("id", id).Find(&found)
		if err != nil || !ok {
			t.Fatalf("读取记录失败：%v %v", ok, err)
		}
		after := time.Now().UnixMilli()
		if found.CreatedAt < before || found.CreatedAt > after || found.UpdatedAt < found.CreatedAt || found.UpdatedAt > after {
			t.Fatalf("持久化时间不是有效毫秒：%+v", found)
		}
		return &found
	}
	created := check(record.ID)
	createdAt := created.CreatedAt
	created.Name = "更新"
	if err := created.Save(); err != nil {
		t.Fatal(err)
	}
	if check(record.ID).CreatedAt != createdAt {
		t.Fatal("更新不得改变创建时间")
	}
	if err := created.Delete(); err != nil {
		t.Fatal(err)
	}
	deleted := check(record.ID)
	if deleted.DeletedAt == nil || *deleted.DeletedAt < before || *deleted.DeletedAt > time.Now().UnixMilli() {
		t.Fatal("软删除时间未按毫秒持久化")
	}
	if err := deleted.Restore(); err != nil {
		t.Fatal(err)
	}
	if check(record.ID).DeletedAt != nil {
		t.Fatal("恢复应清空软删除时间")
	}

	err = database.TransactionContext(ctx, func(tx *db.Tx) error {
		owner := &milliRecord{Name: "事务模型"}
		if _, err := db.NewModelFor(ctx, database, owner, db.WithModelTransaction(tx)); err != nil {
			return err
		}
		if err := owner.Save(); err != nil {
			return err
		}
		if _, err := tx.Name("milli_records").Where("id", owner.ID).Update(map[string]any{"name": "事务更新"}); err != nil {
			return err
		}
		_, err := tx.Name("milli_records").InsertAll([]map[string]any{{"name": "批量一"}, {"name": "批量二"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var records []*milliRecord
	if err := record.Query().Order("id").Select(&records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 {
		t.Fatalf("事务或批量写入不完整：%d", len(records))
	}
	for _, row := range records {
		check(row.ID)
	}
	rollback := errors.New("主动回滚测试")
	if err := database.TransactionContext(ctx, func(tx *db.Tx) error {
		if _, err := tx.Name("milli_records").Insert(map[string]any{"name": "不应提交"}); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("事务应返回原错误：%v", err)
	}
	count, err := record.Query().Count()
	if err != nil || count != 4 {
		t.Fatalf("事务没有完整回滚：%d %v", count, err)
	}
	count, err = record.Query().WhereTime("created_at", "=", time.UnixMilli(createdAt)).Count()
	if err != nil || count < 1 {
		t.Fatalf("毫秒查询条件无法读取实际记录：%d %v", count, err)
	}
}
