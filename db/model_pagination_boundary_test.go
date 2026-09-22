package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestTypedCursorRawKeyAndProjection 验证获取器不改变游标、记录身份或额外探测行的行为。
func TestTypedCursorRawKeyAndProjection(t *testing.T) {
	query, connection := newTypedPageQuery()
	getterCalls := 0
	if err := query.model.Getter("id", func(value any, _ map[string]any) any {
		getterCalls++
		return value.(int64) + 100
	}); err != nil {
		t.Fatal(err)
	}
	page, err := SeekPage[*typedPageRecord](query, 1, nil)
	if err != nil || page.List[0].ID != 101 || page.NextCursor != int64(1) || page.List[0].record.identity != int64(1) || getterCalls != 1 {
		t.Fatalf("获取器污染游标或记录身份: %#v calls=%d err=%v", page, getterCalls, err)
	}
	if connection.counts != 0 || len(connection.selects) != 1 || connection.selects[0].Limit() != 2 || connection.selects[0].Offset() != 0 {
		t.Fatalf("游标执行了额外查询: counts=%d selects=%v", connection.counts, connection.selects)
	}
	type nameOnly struct{ Name string }
	projection, err := SeekPage[nameOnly](query, 1, nil)
	if err != nil || projection.NextCursor != int64(1) || projection.List[0].Name != "甲" {
		t.Fatalf("DTO 必须能够省略内部游标字段: %#v %v", projection, err)
	}
}

// TestTypedCursorValidation 验证游标所有前置约束在数据库访问前生效。
func TestTypedCursorValidation(t *testing.T) {
	query, connection := newTypedPageQuery()
	var nilKey *int64
	for _, input := range []struct {
		size  int
		field string
		codec CursorCodec
		after any
	}{
		{maxQueryResultRows, "id", OrderedCursorCodec{}, nil},
		{1, "id; DROP TABLE users", OrderedCursorCodec{}, nil},
		{1, "id", nil, nil},
		{1, "id", OrderedCursorCodec{}, nilKey},
		{1, "id", OrderedCursorCodec{}, "1"},
	} {
		if page, err := SeekPageWithCodec[typedPageDTO](query, input.size, input.field, input.codec, input.after); err == nil || page != nil {
			t.Fatalf("无效游标被接受: %#v %#v %v", input, page, err)
		}
	}
	if _, err := SeekPage[int](query, 1, nil); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("无效投影被接受: %v", err)
	}
	if connection.counts != 0 || len(connection.selects) != 0 {
		t.Fatal("无效游标访问了数据库")
	}
	for _, rows := range [][]map[string]any{
		{{"name": "缺主键"}}, {{"id": nil}}, {{"id": int64(1)}, {"id": int64(1)}},
	} {
		connection.rows = rows
		if page, err := SeekPage[typedPageDTO](query, 1, nil); page != nil || !errors.Is(err, ErrInvalidDatabaseRow) {
			t.Fatalf("无效结果游标被接受: %#v %v", page, err)
		}
	}
}

// TestTypedPagesBatchRelations 验证一页只发一次关联查询，探测行不会进入关联键集合。
func TestTypedPagesBatchRelations(t *testing.T) {
	for _, cursor := range []bool{false, true} {
		query, connection := newTypedPageQuery()
		connection.tableRows = map[string][]map[string]any{
			"profiles": {{"id": int64(10), "user_id": int64(1), "bio": "简介"}},
		}
		if err := query.model.DefineHasOne("profile", NewModel(query.model.db, "profiles"), "user_id", "id"); err != nil {
			t.Fatal(err)
		}
		query = query.model.With("profile").Order("id")
		if cursor {
			page, err := SeekPage[*typedPageRecord](query, 1, nil)
			if err != nil || page.List[0].Profile.Bio != "简介" || connection.counts != 0 {
				t.Fatalf("游标关联错误: %#v %v", page, err)
			}
		} else {
			page, err := Paginate[*typedPageRecord](query, 1, 1)
			if err != nil || page.List[0].Profile.Bio != "简介" || connection.counts != 1 {
				t.Fatalf("分页关联错误: %#v %v", page, err)
			}
		}
		if len(connection.selects) != 2 || connection.selects[1].Table() != "profiles" {
			t.Fatalf("关联查询次数错误: %v", connection.selects)
		}
		_, args, err := connection.selects[1].Predicate().compileSQL()
		if err != nil || !reflect.DeepEqual(args, []any{int64(1)}) {
			t.Fatalf("额外探测行进入关联键: %v %v", args, err)
		}
	}
}

// TestTypedChunksQueryBudget 验证完整末批不会产生额外空查询，游标不会执行 COUNT 或 OFFSET。
func TestTypedChunksQueryBudget(t *testing.T) {
	for _, cursor := range []bool{false, true} {
		connection := &chunkRecorderConn{pages: [][]map[string]any{
			{{"id": int64(1)}, {"id": int64(2)}, {"id": int64(3)}},
			{{"id": int64(3)}, {"id": int64(4)}},
		}}
		query := NewModel(NewDB(connection), "users").Query()
		var ids []int64
		consume := func(rows []typedPageDTO) (bool, error) {
			for _, row := range rows {
				ids = append(ids, row.ID)
			}
			return true, nil
		}
		var err error
		if cursor {
			err = ChunkById(query, 2, consume)
		} else {
			err = Chunk(query, 2, consume)
		}
		if err != nil || !reflect.DeepEqual(ids, []int64{1, 2, 3, 4}) || len(connection.selects) != 2 || connection.countCalls != 0 || !reflect.DeepEqual(connection.limits, []int{3, 3}) {
			t.Fatalf("分批查询预算错误: ids=%v queries=%v err=%v", ids, connection.selects, err)
		}
		if cursor && (!reflect.DeepEqual(connection.offsets, []int{0, 0}) || !reflect.DeepEqual(connection.whereArgs[1], []any{int64(2)})) {
			t.Fatalf("游标出现偏移或错误边界: %v %v", connection.offsets, connection.whereArgs)
		}
		if !cursor && !reflect.DeepEqual(connection.offsets, []int{0, 2}) {
			t.Fatalf("偏移批次页大小错误: %v", connection.offsets)
		}
		if query.query.order != "" || query.query.limit != 0 || query.query.offset != 0 {
			t.Fatal("遍历污染了源查询")
		}
	}
}

// TestTypedChunksFailureBoundaries 验证前置校验、空集和映射失败不会调用业务回调。
func TestTypedChunksFailureBoundaries(t *testing.T) {
	for _, cursor := range []bool{false, true} {
		query, connection := newTypedPageQuery()
		run := func(size int, consume func([]typedPageDTO) (bool, error)) error {
			if cursor {
				return ChunkById(query, size, consume)
			}
			return Chunk(query, size, consume)
		}
		calls := 0
		consume := func([]typedPageDTO) (bool, error) { calls++; return false, nil }
		if err := run(1, nil); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("空回调错误: %v", err)
		}
		if err := run(maxQueryResultRows, consume); !errors.Is(err, ErrInvalidPagination) {
			t.Fatalf("分批大小错误: %v", err)
		}
		if len(connection.selects) != 0 {
			t.Fatal("无效分批输入访问数据库")
		}
		connection.rows = nil
		if err := run(0, consume); err != nil || calls != 0 || connection.selects[0].Limit() != DefaultChunkSize+1 {
			t.Fatalf("空集或默认大小错误: calls=%d err=%v", calls, err)
		}
		connection.rows = []map[string]any{{"id": int64(1), "name": "正常"}, {"id": int64(2), "name": []int{1}}}
		if err := run(2, consume); !errors.Is(err, ErrInvalidDatabaseRow) || calls != 0 {
			t.Fatalf("失败批次部分发布: calls=%d err=%v", calls, err)
		}
		failure := errors.New("测试读取失败")
		connection.readErr = failure
		if err := run(2, consume); !errors.Is(err, failure) {
			t.Fatalf("读取错误未传播: %v", err)
		}
		query = nil
		if err := run(1, consume); !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("空查询错误: %v", err)
		}
	}
}

type typedTextCursorCodec struct{}

// Compare 对应 SQLite BINARY 文本排序，业务使用时须匹配实际数据库排序规则。
func (typedTextCursorCodec) Compare(a, b any) (int, error) {
	left, leftOK := a.(string)
	right, rightOK := b.(string)
	if !leftOK || !rightOK {
		return 0, ErrUnsupportedCursorKey
	}
	return strings.Compare(left, right), nil
}

// TestTypedTextCursorSQLite 验证显式 codec 和自定义主键可完成真实多批遍历。
func TestTypedTextCursorSQLite(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	if _, err := raw.Exec("CREATE TABLE text_pages (code TEXT PRIMARY KEY COLLATE BINARY, name TEXT); INSERT INTO text_pages VALUES ('a','甲'),('b','乙'),('c','丙')"); err != nil {
		t.Fatal(err)
	}
	model := NewModel(database, "text_pages").PrimaryKey("code")
	if _, err := SeekPage[typedPageDTO](model.Query(), 1, nil); !errors.Is(err, ErrUnsupportedCursorKey) {
		t.Fatalf("默认 codec 不能推断文本排序: %v", err)
	}
	var names []string
	err := ChunkByIdWithCodec(model.Query(), 1, "code", typedTextCursorCodec{}, func(rows []typedPageDTO) (bool, error) {
		names = append(names, rows[0].Name)
		return true, nil
	})
	if err != nil || !reflect.DeepEqual(names, []string{"甲", "乙", "丙"}) {
		t.Fatalf("文本主键遍历错误: %v %v", names, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SeekPage[typedPageDTO](model.Query().WithContext(ctx), 1, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("游标未继承取消状态: %v", err)
	}
}
