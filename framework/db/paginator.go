package db

import "math"

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
func (p *Paginator) Offset() int {
	if p == nil || p.Page <= 1 || p.PageSize <= 0 {
		return 0
	}
	return (p.Page - 1) * p.PageSize
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
func buildPaginator(list []map[string]interface{}, total int64, page int, pageSize int) *Paginator {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}

	lastPage := 0
	if total > 0 {
		lastPage = int(math.Ceil(float64(total) / float64(pageSize)))
	}

	return &Paginator{
		List:     list,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		LastPage: lastPage,
		HasMore:  page < lastPage,
	}
}
