package db

import "fmt"

// CursorPage 表示不依赖总数和 OFFSET 的稳定游标分页结果。
// NextCursor 只在 HasMore 为 true 时返回，调用方应将其编码后传给下一次请求。
type CursorPage[T any] struct {
	List       []T  `json:"list"`
	NextCursor any  `json:"next_cursor"`
	PageSize   int  `json:"page_size"`
	HasMore    bool `json:"has_more"`
}

// ToMap 将游标分页结果转换为业务层常用的响应结构。
func (p *CursorPage[T]) ToMap() map[string]interface{} {
	if p == nil {
		return map[string]interface{}{
			"list":        []T{},
			"next_cursor": nil,
			"page_size":   DefaultPageSize,
			"has_more":    false,
		}
	}
	return map[string]interface{}{
		"list":        p.List,
		"next_cursor": p.NextCursor,
		"page_size":   p.PageSize,
		"has_more":    p.HasMore,
	}
}

// SeekPage 使用主键游标读取一页数据，不执行 COUNT，也不使用 OFFSET。
// pkField 必须是结果中存在的唯一、非空且严格递增的字段；首个页面的 after 传 nil。
func (q *Query) SeekPage(pageSize int, pkField string, after interface{}) (*CursorPage[map[string]any], error) {
	return q.SeekPageWithCodec(pageSize, pkField, OrderedCursorCodec{}, after)
}

// SeekPageWithCodec 使用显式排序 codec 执行游标分页，支持文本、二进制和自定义主键。
func (q *Query) SeekPageWithCodec(pageSize int, pkField string, codec CursorCodec, after interface{}) (*CursorPage[map[string]any], error) {
	if err := q.ensureValid(); err != nil {
		return nil, q.reportError("seek_page", err, nil)
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	if pageSize >= maxQueryResultRows {
		return nil, q.reportError("seek_page", fmt.Errorf("%w: pageSize 超过 %d", ErrInvalidPagination, maxQueryResultRows-1), nil)
	}
	maxInt := int(^uint(0) >> 1)
	if pageSize >= maxInt {
		return nil, q.reportError("seek_page", fmt.Errorf("%w: 游标分页 pageSize 溢出", ErrInvalidPagination), nil)
	}
	if isNilDatabaseDependency(codec) {
		return nil, q.reportError("seek_page", ErrUnsupportedCursorKey, nil)
	}
	if pkField == "" {
		pkField = "id"
	}
	if err := validateIdentifier(pkField); err != nil {
		return nil, q.reportError("seek_page", fmt.Errorf("unsafe pk field: %w", err), nil)
	}
	hasAfter := after != nil
	if hasAfter && isNilDatabaseDependency(after) {
		return nil, q.reportError("seek_page", fmt.Errorf("%w: after 游标不能为空", ErrInvalidDatabaseRow), nil)
	}
	if hasAfter {
		if err := validateCursorBoundary(after, codec); err != nil {
			return nil, q.reportError("seek_page", err, nil)
		}
	}

	query := q.clone()
	if hasAfter {
		query = query.appendWhereField(pkField, ">", cloneDatabaseValue(after))
		if query.err != nil {
			return nil, q.reportError("seek_page", query.err, nil)
		}
	}
	query.order = pkField
	query.limit = pageSize + 1
	query.offset = 0

	rows, err := query.Select()
	if err != nil {
		return nil, err
	}
	if err := validateCursorRows(rows, pkField, after, codec); err != nil {
		return nil, q.reportError("seek_page", err, nil)
	}

	hasMore := len(rows) > pageSize
	if hasMore {
		visible := rows[:pageSize]
		nextCursor := cloneDatabaseValue(visible[len(visible)-1][pkField])
		return &CursorPage[map[string]any]{
			List:       visible,
			NextCursor: nextCursor,
			PageSize:   pageSize,
			HasMore:    true,
		}, nil
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	return &CursorPage[map[string]any]{
		List:     rows,
		PageSize: pageSize,
		HasMore:  false,
	}, nil
}

// validateCursorBoundary 验证边界值能被 codec 处理且自比较结果稳定。
func validateCursorBoundary(value interface{}, codec CursorCodec) error {
	if value == nil || isNilDatabaseDependency(value) {
		return fmt.Errorf("%w: 游标值不能为空", ErrInvalidDatabaseRow)
	}
	comparison, err := codec.Compare(value, value)
	if err != nil {
		return err
	}
	if comparison != 0 {
		return fmt.Errorf("%w: 游标 codec 自比较必须相等", ErrInvalidDatabaseRow)
	}
	return nil
}

// validateCursorRows 验证结果中的游标存在，并且按数据库声明的顺序严格递增。
func validateCursorRows(rows []map[string]interface{}, pkField string, previous interface{}, codec CursorCodec) error {
	if len(rows) == 0 {
		return nil
	}
	if previous != nil {
		if err := validateCursorBoundary(previous, codec); err != nil {
			return err
		}
	}

	var last interface{}
	for index, row := range rows {
		current, ok := row[pkField]
		if !ok || current == nil || isNilDatabaseDependency(current) {
			return fmt.Errorf("%w: 第 %d 行缺少游标字段 %q", ErrInvalidDatabaseRow, index, pkField)
		}
		current = cloneDatabaseValue(current)
		if index == 0 {
			if previous == nil {
				if err := validateCursorBoundary(current, codec); err != nil {
					return err
				}
			} else {
				comparison, err := codec.Compare(previous, current)
				if err != nil {
					return err
				}
				if comparison >= 0 {
					return fmt.Errorf("%w: 游标未严格递增", ErrInvalidDatabaseRow)
				}
			}
		} else {
			comparison, err := codec.Compare(last, current)
			if err != nil {
				return err
			}
			if comparison >= 0 {
				return fmt.Errorf("%w: 第 %d 行游标未严格递增", ErrInvalidDatabaseRow, index)
			}
		}
		last = current
	}
	return nil
}

// cursorPageLastCursor 返回已经完成校验的最后一个游标，供 ChunkById 保存下一批边界。
func cursorPageLastCursor(rows []map[string]interface{}, pkField string, previous interface{}, codec CursorCodec) (interface{}, error) {
	if err := validateCursorRows(rows, pkField, previous, codec); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return cloneDatabaseValue(rows[len(rows)-1][pkField]), nil
}
