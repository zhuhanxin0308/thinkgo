package db

import "fmt"

// Paginator 封装分页结果，统一输出列表、总数和页码元信息。
type Paginator struct {
	List     []map[string]interface{}
	Total    int64
	Page     int
	PageSize int
	LastPage int
	HasMore  bool
}

// Offset 返回当前分页的偏移量，便于复用到自定义查询场景。
func (p *Paginator) Offset() (int, error) {
	if p == nil || p.Page <= 1 || p.PageSize <= 0 {
		return 0, nil
	}
	if p.Page-1 > int(^uint(0)>>1)/p.PageSize {
		return 0, fmt.Errorf("%w: 分页偏移量溢出", ErrInvalidPagination)
	}
	return (p.Page - 1) * p.PageSize, nil
}

// ToMap 转换为业务层常用的响应结构。
func (p *Paginator) ToMap() map[string]interface{} {
	if p == nil {
		return map[string]interface{}{
			"list":      []map[string]interface{}{},
			"total":     int64(0),
			"page":      1,
			"page_size": DefaultPageSize,
			"last_page": 0,
			"has_more":  false,
		}
	}

	return map[string]interface{}{
		"list":      p.List,
		"total":     p.Total,
		"page":      p.Page,
		"page_size": p.PageSize,
		"last_page": p.LastPage,
		"has_more":  p.HasMore,
	}
}

// buildPaginator 根据总数和分页参数构建分页对象。
func buildPaginator(list []map[string]interface{}, total int64, page int, pageSize int) (*Paginator, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	if total < 0 {
		return nil, fmt.Errorf("%w: 总记录数不能为负数", ErrInvalidPagination)
	}
	if page > 1 && page-1 > int(^uint(0)>>1)/pageSize {
		return nil, fmt.Errorf("%w: 分页偏移量溢出", ErrInvalidPagination)
	}

	lastPage64 := int64(0)
	if total > 0 {
		lastPage64 = total / int64(pageSize)
		if total%int64(pageSize) != 0 {
			lastPage64++
		}
	}
	if uint64(lastPage64) > uint64(^uint(0)>>1) {
		return nil, fmt.Errorf("%w: 总页数超出 int 范围", ErrInvalidPagination)
	}
	lastPage := int(lastPage64)

	return &Paginator{
		List:     list,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		LastPage: lastPage,
		HasMore:  page < lastPage,
	}, nil
}
