package db

import (
	"context"
	"reflect"
)

// Query 创建无筛选的独立模型查询，便于传入类型化分页和遍历函数。
func (m *Model) Query() *ModelQuery { return m.newModelQuery() }

// Paginate 返回结构体或结构体指针列表及分页信息，先校验再执行 COUNT 和列表查询。
// 查询条件、事务、软删除和 With 关联保持一致；稳定分页应显式指定唯一排序。
// COUNT 与列表是两次查询，需要一致快照时由调用方显式绑定具有相应隔离级别的事务。
func Paginate[T any](query *ModelQuery, page, pageSize int) (*Paginator[T], error) {
	scanner, err := newTypedModelScanner[T](query)
	if err != nil {
		return nil, err
	}
	data := query.clone()
	data.query = data.query.Page(page, pageSize)
	if err := data.validationError(); err != nil {
		return nil, err
	}
	if page < 1 {
		page = 1
	}
	pageSize = data.query.limit
	total, err := query.Count()
	if err != nil {
		return nil, err
	}
	result, err := buildPaginator[T](nil, total, page, pageSize)
	if err != nil {
		return nil, err
	}
	rows, raw, err := data.selectRecordRows()
	if err != nil {
		return nil, err
	}
	result.List, err = scanner.scan(data, rows, raw)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// SeekPage 按模型主键升序读取类型化分页，不执行 COUNT 或 OFFSET。
// 显式 With 关联只为当前可见页批量加载，额外探测行不执行获取器或关联查询。
func SeekPage[T any](query *ModelQuery, pageSize int, after any) (*CursorPage[T], error) {
	return SeekPageWithCodec[T](query, pageSize, "", OrderedCursorCodec{}, after)
}

// SeekPageWithCodec 使用与数据库排序规则一致的 codec，支持文本和自定义游标列。
// 游标列必须唯一、非空并包含在数据库投影中；DTO 可以不声明游标字段。
// NextCursor 保留数据库原始类型，不能用获取器转换后的展示字段替代。
func SeekPageWithCodec[T any](query *ModelQuery, pageSize int, pkField string, codec CursorCodec, after any) (*CursorPage[T], error) {
	scanner, err := newTypedModelScanner[T](query)
	if err != nil {
		return nil, err
	}
	return scanner.seek(query, pageSize, pkField, codec, after)
}

// typedModelScanner 在遍历开始时验证一次元数据，每批仍检查上下文并隔离记录拥有者。
type typedModelScanner[T any] struct {
	ctx       context.Context
	sliceType reflect.Type
	metadata  modelMetadata
}

func newTypedModelScanner[T any](query *ModelQuery) (typedModelScanner[T], error) {
	var rows []T
	destination, metadata, err := modelScanDestination(&rows, true)
	if err != nil {
		return typedModelScanner[T]{}, err
	}
	ctx, err := query.modelScanContext()
	if err != nil {
		return typedModelScanner[T]{}, err
	}
	return typedModelScanner[T]{ctx: ctx, sliceType: destination.Type(), metadata: metadata}, nil
}

func (s typedModelScanner[T]) scan(query *ModelQuery, rows, raw []map[string]any) ([]T, error) {
	result, err := query.scanModelRows(s.ctx, s.sliceType, s.metadata, rows, raw)
	if err != nil {
		return nil, err
	}
	return result.Interface().([]T), nil
}

func (s typedModelScanner[T]) seek(query *ModelQuery, pageSize int, pkField string, codec CursorCodec, after any) (*CursorPage[T], error) {
	if pkField == "" {
		pkField = query.model.primaryKeyField()
	}
	page, err := query.prepareQuery().SeekPageWithCodec(pageSize, pkField, codec, after)
	if err != nil {
		return nil, err
	}
	rows, raw, err := query.prepareRecordRows(page.List)
	if err != nil {
		return nil, err
	}
	list, err := s.scan(query, rows, raw)
	if err != nil {
		return nil, err
	}
	return &CursorPage[T]{List: list, NextCursor: page.NextCursor, PageSize: page.PageSize, HasMore: page.HasMore}, nil
}
