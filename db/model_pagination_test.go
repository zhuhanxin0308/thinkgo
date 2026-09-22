package db

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type typedPageDTO struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// typedPageConnection 记录实际请求，验证无效输入不会触发查询及失败后的发布边界。
type typedPageConnection struct {
	modelBusinessConnection
	selects  []SelectRequest
	counts   int
	readErr  error
	countErr error
}

func (c *typedPageConnection) Select(ctx context.Context, request SelectRequest) ([]map[string]any, error) {
	c.selects = append(c.selects, request)
	if c.readErr != nil {
		return nil, c.readErr
	}
	return c.modelBusinessConnection.Select(ctx, request)
}

func (c *typedPageConnection) Count(ctx context.Context, request CountRequest) (int64, error) {
	c.counts++
	if c.countErr != nil {
		return 0, c.countErr
	}
	return c.modelBusinessConnection.Count(ctx, request)
}

func newTypedPageQuery() (*ModelQuery, *typedPageConnection) {
	connection := &typedPageConnection{modelBusinessConnection: modelBusinessConnection{
		rows: []map[string]any{{"id": int64(1), "name": "甲"}, {"id": int64(2), "name": "乙"}}, count: 2,
	}}
	return NewModel(NewDB(connection), "users").Order("id"), connection
}

// TestTypedPaginateContract 验证类型化分页、查询复用和可直接返回的 JSON 元信息。
func TestTypedPaginateContract(t *testing.T) {
	query, connection := newTypedPageQuery()
	page, err := Paginate[typedPageDTO](query, 2, 1)
	if err != nil || page.Total != 2 || page.Page != 2 || page.PageSize != 1 || page.LastPage != 2 || page.HasMore || !reflect.DeepEqual(page.List, []typedPageDTO{{ID: 2, Name: "乙"}}) {
		t.Fatalf("类型化分页错误: page=%#v err=%v", page, err)
	}
	if connection.counts != 1 || len(connection.selects) != 1 || connection.selects[0].Offset() != 1 || query.query.limit != 0 || query.query.offset != 0 {
		t.Fatalf("分页查询次数或源查询状态错误: counts=%d selects=%v", connection.counts, connection.selects)
	}
	encoded, err := json.Marshal(page)
	if err != nil || string(encoded) != `{"list":[{"id":2,"name":"乙"}],"total":2,"page":2,"page_size":1,"last_page":2,"has_more":false}` {
		t.Fatalf("分页 JSON 契约错误: %s %v", encoded, err)
	}
	if page.ToMap()["list"].([]typedPageDTO)[0].ID != 2 {
		t.Fatal("ToMap 丢失了列表类型")
	}
	empty, err := Paginate[typedPageDTO](query, 3, 1)
	if err != nil || empty.List == nil || len(empty.List) != 0 || empty.Total != 2 || empty.HasMore {
		t.Fatalf("超出末页的结果错误: %#v %v", empty, err)
	}
}

// TestTypedPaginateRejectsBeforeDatabase 验证类型、分页和取消错误不会浪费数据库请求。
func TestTypedPaginateRejectsBeforeDatabase(t *testing.T) {
	query, connection := newTypedPageQuery()
	for _, input := range [][2]int{{1, maxQueryResultRows + 1}, {int(^uint(0) >> 1), 2}} {
		if page, err := Paginate[typedPageDTO](query, input[0], input[1]); page != nil || !errors.Is(err, ErrInvalidPagination) {
			t.Fatalf("接受无效分页: page=%#v err=%v", page, err)
		}
	}
	if _, err := Paginate[int](query, 1, 1); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("接受标量记录: %v", err)
	}
	if _, err := Paginate[modelScanRecord](query, 1, 1); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("接受有状态模型值切片: %v", err)
	}
	if _, err := Paginate[typedPageDTO](nil, 1, 1); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("空查询未报错: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Paginate[typedPageDTO](query.WithContext(ctx), 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未传播: %v", err)
	}
	if connection.counts != 0 || len(connection.selects) != 0 {
		t.Fatalf("无效输入访问了数据库: counts=%d selects=%d", connection.counts, len(connection.selects))
	}
}

// TestTypedPaginateErrorsAndDefaults 验证计数、查询和行转换任一步失败都不发布分页结果。
func TestTypedPaginateErrorsAndDefaults(t *testing.T) {
	query, connection := newTypedPageQuery()
	page, err := Paginate[*typedPageDTO](query, 0, 0)
	if err != nil || page.Page != 1 || page.PageSize != DefaultPageSize || len(page.List) != 2 || page.List[0] == page.List[1] {
		t.Fatalf("默认参数或指针列表错误: %#v %v", page, err)
	}
	failure := errors.New("测试数据库失败")
	connection.countErr = failure
	if page, err := Paginate[typedPageDTO](query, 1, 1); page != nil || !errors.Is(err, failure) {
		t.Fatalf("计数失败被忽略: %#v %v", page, err)
	}
	connection.countErr, connection.readErr = nil, failure
	if page, err := Paginate[typedPageDTO](query, 1, 1); page != nil || !errors.Is(err, failure) {
		t.Fatalf("查询失败被忽略: %#v %v", page, err)
	}
	connection.readErr = nil
	connection.rows[1]["id"] = "无效整数"
	if page, err := Paginate[typedPageDTO](query, 1, 2); page != nil || !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("转换失败发布了部分结果: %#v %v", page, err)
	}
}
