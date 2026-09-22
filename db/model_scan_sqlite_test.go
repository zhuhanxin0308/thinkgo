package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// newModelScanSQLite 使用真实驱动验证列类型、NULL 与关闭边界，仅无 CGO 时跳过。
func newModelScanSQLite(t *testing.T) (*DB, *sql.DB) {
	t.Helper()
	raw, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	raw.SetMaxOpenConns(1)
	if err := raw.Ping(); err != nil {
		_ = raw.Close()
		if strings.Contains(err.Error(), "CGO_ENABLED=0") || strings.Contains(err.Error(), "requires cgo") {
			t.Skipf("SQLite 结构体映射需要 CGO: %v", err)
		}
		t.Fatalf("连接 SQLite 失败: %v", err)
	}
	database := NewDB(NewSQLConnection(raw, &builder.Sqlite{}))
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("关闭 SQLite 失败: %v", err)
		}
	})
	_, err = raw.Exec(`CREATE TABLE scan_records (
		id INTEGER PRIMARY KEY,
		display_name TEXT NOT NULL,
		note TEXT NULL,
		score INTEGER NULL,
		payload BLOB NULL,
		created DATETIME NOT NULL,
		delete_time DATETIME NULL
	)`)
	if err != nil {
		t.Fatalf("创建扫描测试表失败: %v", err)
	}
	return database, raw
}

// TestModelScanSQLiteNativeTypesAndModelPipeline 验证真实查询同时保留 Getter、软删除和时区。
func TestModelScanSQLiteNativeTypesAndModelPipeline(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	location := time.FixedZone("测试业务时区", 8*60*60)
	database.SetLocation(location)
	instant := time.Date(2026, time.September, 6, 12, 30, 0, 123456000, time.UTC)
	for _, record := range []struct {
		id      int64
		deleted any
	}{
		{id: 1}, {id: 2, deleted: instant},
	} {
		_, err := raw.Exec("INSERT INTO scan_records(id,display_name,note,score,payload,created,delete_time) VALUES(?,?,?,?,?,?,?)",
			record.id, "用户", nil, nil, []byte{1, 2, 3}, instant, record.deleted)
		if err != nil {
			t.Fatalf("插入扫描测试记录失败: %v", err)
		}
	}
	model := NewModel(database, "scan_records").SoftDelete()
	if err := model.Getter("display_name", func(value any, _ map[string]any) any {
		return "已读取:" + value.(string)
	}); err != nil {
		t.Fatal(err)
	}
	var record modelScanRecord
	found, err := model.newModelQuery().Find(&record)
	if err != nil || !found || record.ID != 1 || record.Name != "已读取:用户" || record.Note != nil || record.Score.Valid || !record.Created.Equal(instant) || record.Created.Location() != location {
		t.Fatalf("真实 SQLite 映射错误: found=%v record=%#v err=%v", found, record, err)
	}
	if record.Model == nil {
		t.Fatal("查询得到的模型记录没有完成实例绑定")
	}
	var records []*modelScanRecord
	if err := model.newModelQuery().Order("id ASC").Select(&records); err != nil || len(records) != 1 || records[0].ID != 1 {
		t.Fatalf("软删除过滤没有进入结构体查询: records=%#v err=%v", records, err)
	}
	if err := model.WithTrashed().Order("id ASC").Select(&records); err != nil || len(records) != 2 || records[1].ID != 2 {
		t.Fatalf("显式包含已删除记录失败: records=%#v err=%v", records, err)
	}
}

// TestModelScanSQLiteConversionAndQueryErrorsAreAtomic 验证真实驱动错误不会发布部分记录。
func TestModelScanSQLiteConversionAndQueryErrorsAreAtomic(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	_, err := raw.Exec("INSERT INTO scan_records(id,display_name,created,score) VALUES(?,?,?,?),(?,?,?,?)",
		1, "正常", time.Now(), 1, 2, "溢出", time.Now(), 128)
	if err != nil {
		t.Fatal(err)
	}
	type smallRecord struct {
		ID    int64
		Score int8
	}
	model := NewModel(database, "scan_records")
	original := smallRecord{ID: 99, Score: 5}
	rows := []smallRecord{original}
	if err := model.newModelQuery().Order("id ASC").Select(&rows); !errors.Is(err, ErrInvalidDatabaseRow) || len(rows) != 1 || rows[0] != original {
		t.Fatalf("后续行溢出污染了原切片: rows=%#v err=%v", rows, err)
	}
	target := smallRecord{ID: 88, Score: 6}
	if _, err := NewModel(database, "missing_scan_table").newModelQuery().Find(&target); err == nil || target.ID != 88 || target.Score != 6 {
		t.Fatalf("数据库错误污染了目标: target=%#v err=%v", target, err)
	}
	duplicate := struct{ DuplicateID int64 }{DuplicateID: 9}
	_, err = model.newModelQuery().Field("id AS duplicate_id, id AS duplicate_id").Find(&duplicate)
	if !errors.Is(err, ErrInvalidDatabaseRow) || duplicate.DuplicateID != 9 {
		t.Fatalf("重复结果列没有被安全拒绝: target=%#v err=%v", duplicate, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := model.newModelQuery().WithContext(ctx).Find(&target); !errors.Is(err, context.Canceled) || target.ID != 88 {
		t.Fatalf("真实驱动取消边界错误: target=%#v err=%v", target, err)
	}
}

// TestModelScanSQLiteRollbackDoesNotLoseDirtyChanges 验证派生事务回滚后不能把未落库修改报告为保存成功。
func TestModelScanSQLiteRollbackDoesNotLoseDirtyChanges(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	if _, err := raw.Exec("INSERT INTO scan_records(id,display_name,created) VALUES(?,?,?)", 1, "原值", time.Now()); err != nil {
		t.Fatal(err)
	}
	target := struct {
		*Model
		ID   int64
		Name string `thinkgo:"display_name"`
	}{}
	found, err := NewModel(database, "scan_records").newModelQuery().Find(&target)
	if err != nil || !found {
		t.Fatalf("加载回滚测试记录失败: %v", err)
	}
	transaction, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	target.Name = "待保存"
	if err := target.WithTx(transaction).Save(); err != nil {
		t.Fatalf("事务内保存失败: %v", err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	// 明确拒绝失效记录可以要求调用方重新查询；返回成功则必须真正写入当前值。
	if err := target.Save(); err != nil {
		if !errors.Is(err, ErrPartialWrite) && !errors.Is(err, ErrInvalidTransaction) && !errors.Is(err, ErrTransactionDone) {
			t.Fatalf("回滚后记录没有返回可识别的状态错误: %v", err)
		}
		return
	}
	actual, err := database.Table("scan_records").Where("id = ?", 1).Value("display_name")
	if err != nil || actual != target.Name {
		t.Fatalf("事务回滚后脏字段被当成已保存: actual=%#v target=%q err=%v", actual, target.Name, err)
	}
}
