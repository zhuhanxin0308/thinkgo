package db

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
)

type typedPageRecord struct {
	*Model
	ID      int64
	Name    string
	Note    *string
	Profile *modelScanProfileDTO `thinkgo:"profile,readonly"`
}

// newTypedPageSQLite 使用真实事务和唯一主键，避免用模拟结果掩盖分页与游标条件错误。
func newTypedPageSQLite(t *testing.T) *Model {
	t.Helper()
	database, raw := newModelScanSQLite(t)
	for _, statement := range []string{
		"CREATE TABLE page_users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, note TEXT NULL, delete_time DATETIME NULL)",
		"CREATE TABLE page_profiles (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, bio TEXT NOT NULL)",
		"INSERT INTO page_users VALUES (1, '甲', '备注', NULL), (2, '乙', NULL, NULL), (3, '已删', NULL, '2026-09-01'), (4, '丁', NULL, NULL)",
		"INSERT INTO page_profiles VALUES (10, 1, '简介甲'), (20, 2, '简介乙'), (40, 4, '简介丁')",
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	model := NewModel(database, "page_users").AutoTimestamp(false).SoftDelete()
	if err := model.DefineHasOne("profile", NewModel(database, "page_profiles"), "user_id", "id"); err != nil {
		t.Fatal(err)
	}
	return model
}

// TestTypedPaginationSQLiteRecordLifecycle 验证所有分页入口保留关联、部分字段和记录保存语义。
func TestTypedPaginationSQLiteRecordLifecycle(t *testing.T) {
	model := newTypedPageSQLite(t)
	query := model.With("profile").Order("id")
	page, err := Paginate[*typedPageRecord](query, 1, 2)
	if err != nil || page.Total != 3 || !page.HasMore || len(page.List) != 2 || page.List[0].Profile.Bio != "简介甲" {
		t.Fatalf("真实分页或关联失败: %#v %v", page, err)
	}
	page.List[0].Name = "更新甲"
	if err := page.List[0].Save(); err != nil {
		t.Fatal(err)
	}
	first, err := SeekPage[*typedPageRecord](query.Field("id,name"), 1, nil)
	if err != nil || first.NextCursor != int64(1) || first.List[0].Name != "更新甲" || first.List[0].Profile.Bio != "简介甲" {
		t.Fatalf("游标页未继承模型行为: %#v %v", first, err)
	}
	note := "未加载的字段"
	first.List[0].Note = &note
	if err := first.List[0].Save(); !errors.Is(err, ErrModelFieldNotLoaded) {
		t.Fatalf("分页记录允许隐式覆盖未加载字段: %v", err)
	}
	second, err := SeekPage[typedPageDTO](query, 2, first.NextCursor)
	if err != nil || second.HasMore || second.NextCursor != nil || !reflect.DeepEqual(second.List, []typedPageDTO{{ID: 2, Name: "乙"}, {ID: 4, Name: "丁"}}) {
		t.Fatalf("游标末页或软删除错误: %#v %v", second, err)
	}
	empty, err := SeekPage[typedPageDTO](query, 2, int64(4))
	if err != nil || empty.List == nil || len(empty.List) != 0 || empty.HasMore {
		t.Fatalf("游标空页错误: %#v %v", empty, err)
	}
}

// TestTypedPaginationSQLiteTransaction 验证父记录、关联预加载及保存均使用同一事务。
func TestTypedPaginationSQLiteTransaction(t *testing.T) {
	model := newTypedPageSQLite(t)
	rollback := errors.New("测试回滚")
	err := model.db.TransactionContext(context.Background(), func(tx *Tx) error {
		if _, err := tx.Table("page_profiles").Where("id", 10).Update(map[string]any{"bio": "事务简介"}); err != nil {
			return err
		}
		query := model.WithTx(tx).With("profile").Order("id")
		page, err := Paginate[*typedPageRecord](query, 1, 1)
		if err != nil || page.List[0].Profile.Bio != "事务简介" {
			t.Fatalf("分页关联没有继承事务: %#v %v", page, err)
		}
		page.List[0].Name = "事务名称"
		if err := page.List[0].Save(); err != nil {
			return err
		}
		cursor, err := SeekPage[*typedPageRecord](query, 1, nil)
		if err != nil || cursor.List[0].Name != "事务名称" {
			t.Fatalf("游标未读到事务内写入: %#v %v", cursor, err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("事务回滚失败: %v", err)
	}
	page, err := Paginate[*typedPageRecord](model.With("profile").Order("id"), 1, 1)
	if err != nil || page.List[0].Name != "甲" || page.List[0].Profile.Bio != "简介甲" {
		t.Fatalf("事务修改泄漏: %#v %v", page, err)
	}
}

// TestTypedChunksSQLite 验证分批消费、停止、错误传播及调用方保留批次后的独立性。
func TestTypedChunksSQLite(t *testing.T) {
	for _, cursor := range []bool{false, true} {
		t.Run(map[bool]string{false: "偏移", true: "游标"}[cursor], func(t *testing.T) {
			model := newTypedPageSQLite(t)
			query := model.With("profile").Order("id")
			run := func(callback func([]*typedPageRecord) (bool, error)) error {
				if cursor {
					return ChunkById(query, 2, callback)
				}
				return Chunk(query, 2, callback)
			}
			var batches [][]*typedPageRecord
			if err := run(func(rows []*typedPageRecord) (bool, error) {
				batches = append(batches, rows)
				for _, row := range rows {
					if row.Profile == nil {
						t.Fatal("分批处理忽略了显式关联")
					}
				}
				return true, nil
			}); err != nil || len(batches) != 2 || len(batches[0]) != 2 || len(batches[1]) != 1 || batches[0][0].ID != 1 || batches[1][0].ID != 4 {
				t.Fatalf("批次边界或独立性错误: %#v %v", batches, err)
			}
			batches[0][0].Name = "批次结束后保存"
			if err := batches[0][0].Save(); err != nil {
				t.Fatal(err)
			}
			calls := 0
			if err := run(func([]*typedPageRecord) (bool, error) { calls++; return false, nil }); err != nil || calls != 1 {
				t.Fatalf("正常提前结束错误: calls=%d err=%v", calls, err)
			}
			failure := errors.New("导出失败")
			if err := run(func([]*typedPageRecord) (bool, error) { return true, failure }); !errors.Is(err, failure) {
				t.Fatalf("吞掉业务错误: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			query = query.WithContext(ctx)
			if err := run(func([]*typedPageRecord) (bool, error) { cancel(); return false, nil }); !errors.Is(err, context.Canceled) {
				t.Fatalf("最后一次回调取消被当成成功: %v", err)
			}
		})
	}
}

// TestTypedChunkByIdMutation 验证边界在回调前捕获，修改展示字段或删除已读行不会跳过下一批。
func TestTypedChunkByIdMutation(t *testing.T) {
	model := newTypedPageSQLite(t)
	var seen []int64
	err := ChunkById(model.Query(), 1, func(rows []*typedPageRecord) (bool, error) {
		id := rows[0].ID
		seen = append(seen, id)
		rows[0].ID = 999
		_, err := model.Where("id", id).Delete()
		return true, err
	})
	if err != nil || !reflect.DeepEqual(seen, []int64{1, 2, 4}) {
		t.Fatalf("游标被回调修改或删除影响: %v %v", seen, err)
	}
}

// TestTypedPaginateGroupedQuery 验证 GROUP/HAVING 的总数是分组数量，投影不依赖模型主键。
func TestTypedPaginateGroupedQuery(t *testing.T) {
	model := newTypedPageSQLite(t)
	type groupDTO struct {
		Note *string
	}
	query := model.WithTrashed().Field("note").Group("note").HavingRaw("COUNT(*) > ?", 1).Order("note")
	page, err := Paginate[groupDTO](query, 1, 1)
	if err != nil || page.Total != 1 || len(page.List) != 1 || page.List[0].Note != nil {
		t.Fatalf("分组分页计数或投影错误: %#v %v", page, err)
	}
}

// TestTypedPaginationConcurrentQuery 验证共享不可变查询时，每个请求的页码和关联互不污染。
func TestTypedPaginationConcurrentQuery(t *testing.T) {
	model := newTypedPageSQLite(t)
	query := model.With("profile").Order("id")
	const workers = 12
	var wait sync.WaitGroup
	for index := range workers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			page, err := Paginate[*typedPageRecord](query, index%3+1, 1)
			want := []int64{1, 2, 4}[index%3]
			if err != nil || len(page.List) != 1 || page.List[0].ID != want || page.List[0].Profile == nil {
				t.Errorf("并发分页串用状态: index=%d page=%#v err=%v", index, page, err)
			}
		}(index)
	}
	wait.Wait()
}

// TestTypedPaginationDuringDatabaseClose 验证关闭过程允许已开启事务完成所有类型化查询。
func TestTypedPaginationDuringDatabaseClose(t *testing.T) {
	model := newTypedPageSQLite(t)
	ctx, cancel := context.WithTimeout(context.Background(), modelTransactionDrainTimeout)
	defer cancel()
	tx, err := model.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	closed := make(chan error, 1)
	go func() { closed <- model.db.Close() }()
	waitForDatabaseCloseLock(t, model.db)
	query := model.WithContext(ctx).WithTx(tx).With("profile").Order("id")
	if page, err := Paginate[*typedPageRecord](query, 1, 1); err != nil || page.List[0].Profile == nil {
		t.Fatalf("关闭期间事务分页失败: %#v %v", page, err)
	}
	if page, err := SeekPage[*typedPageRecord](query, 1, nil); err != nil || page.List[0].Profile == nil {
		t.Fatalf("关闭期间事务游标失败: %#v %v", page, err)
	}
	if err := ChunkById(query, 1, func(rows []*typedPageRecord) (bool, error) {
		rows[0].Name = "关闭期间保存"
		return true, rows[0].Save()
	}); err != nil {
		t.Fatalf("关闭期间事务批处理失败: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("事务结束后数据库没有完成关闭")
	}
	if _, err := Paginate[typedPageDTO](query, 1, 1); !errors.Is(err, ErrTransactionDone) {
		t.Fatalf("已结束事务仍能查询: %v", err)
	}
}
