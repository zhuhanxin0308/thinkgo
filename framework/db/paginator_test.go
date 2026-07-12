package db

import (
	"errors"
	"testing"
)

type paginateMockConnection struct {
	rows  []map[string]interface{}
	count int64
}

func (m *paginateMockConnection) Select(table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	return m.rows, nil
}

func (m *paginateMockConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	return 0, nil
}

func (m *paginateMockConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (m *paginateMockConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (m *paginateMockConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	return m.count, nil
}

func (m *paginateMockConnection) Close() error {
	return nil
}

func TestQueryPaginate(t *testing.T) {
	mock := &paginateMockConnection{
		rows: []map[string]interface{}{
			{"id": int64(21), "name": "张三"},
			{"id": int64(22), "name": "李四"},
		},
		count: 35,
	}
	database := NewDB(mock)

	paginator, err := database.Table("users").Order("id asc").Paginate(2, 2)
	if err != nil {
		t.Fatalf("Paginate 不应返回错误，实际为 %v", err)
	}
	if paginator.Total != 35 {
		t.Fatalf("分页总数不正确，实际为 %d", paginator.Total)
	}
	if paginator.Page != 2 || paginator.PageSize != 2 {
		t.Fatalf("分页参数不正确，实际为 page=%d pageSize=%d", paginator.Page, paginator.PageSize)
	}
	if paginator.LastPage != 18 {
		t.Fatalf("总页数不正确，实际为 %d", paginator.LastPage)
	}
	if !paginator.HasMore {
		t.Fatal("第二页在总数 35 的情况下应仍然有更多页")
	}
	if len(paginator.List) != 2 {
		t.Fatalf("分页列表长度不正确，实际为 %d", len(paginator.List))
	}
	offset, err := paginator.Offset()
	if err != nil || offset != 2 {
		t.Fatalf("分页偏移量不正确，实际为 %d，错误为 %v", offset, err)
	}
	if paginator.ToMap()["total"] != int64(35) {
		t.Fatalf("ToMap 应输出 total 字段，实际为 %#v", paginator.ToMap())
	}
}

// TestPaginatorUsesExactIntegerArithmetic 验证超过 float64 精确范围的总数仍能正确计算页数。
func TestPaginatorUsesExactIntegerArithmetic(t *testing.T) {
	mock := &paginateMockConnection{count: 1<<53 + 1}
	database := NewDB(mock)
	paginator, err := database.Table("users").Paginate(1, 2)
	if err != nil {
		t.Fatalf("大总数分页失败: %v", err)
	}
	const expected = 4_503_599_627_370_497
	if paginator.LastPage != expected {
		t.Fatalf("总页数发生精度丢失，期望 %d，实际为 %d", expected, paginator.LastPage)
	}
}

// TestPaginatorOffsetRejectsOverflow 验证偏移量溢出会显式失败。
func TestPaginatorOffsetRejectsOverflow(t *testing.T) {
	paginator := &Paginator{Page: int(^uint(0) >> 1), PageSize: 2}
	if _, err := paginator.Offset(); !errors.Is(err, ErrInvalidPagination) {
		t.Fatalf("溢出偏移量应返回 ErrInvalidPagination，实际为 %v", err)
	}
}
