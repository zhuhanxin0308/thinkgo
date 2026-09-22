package db

import "fmt"

// Chunk 按 OFFSET 分批交付独立的类型化列表，显式 With 关联按当前批次加载。
// 回调返回 false 正常停止，返回错误则终止并保留错误链；已消费批次不会自动回滚。
// 未指定排序时使用模型主键；遍历期间会修改筛选结果集的任务应使用 ChunkById。
func Chunk[T any](query *ModelQuery, count int, callback func([]T) (bool, error)) error {
	scanner, count, err := prepareTypedChunk(query, count, callback)
	if err != nil {
		return err
	}
	if query.query.order == "" {
		query = query.Order(query.model.primaryKeyField())
	}
	for page := 1; ; page++ {
		data := query.clone()
		data.query = data.query.Page(page, count)
		if err := data.validationError(); err != nil {
			return err
		}
		// 多取一条仅用于判断下一批，避免整批结束后再发出空查询。
		data.query.limit = count + 1
		rows, err := data.prepareQuery().Select()
		if err != nil {
			return err
		}
		hasMore := len(rows) > count
		if hasMore {
			rows = rows[:count]
		}
		rows, raw, err := data.prepareRecordRows(rows)
		if err != nil {
			return err
		}
		batch, err := scanner.scan(data, rows, raw)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		proceed, err := callback(batch)
		if err != nil {
			return err
		}
		if err := scanner.ctx.Err(); err != nil {
			return err
		}
		if !proceed || !hasMore {
			return nil
		}
	}
}

// ChunkById 按模型主键升序分批读取，回调可保存或删除当前记录，不使用 COUNT 或 OFFSET。
// 游标在获取器和回调运行前捕获；每批拥有独立切片与模型记录，后续批次不会覆盖前批。
func ChunkById[T any](query *ModelQuery, count int, callback func([]T) (bool, error)) error {
	return ChunkByIdWithCodec(query, count, "", OrderedCursorCodec{}, callback)
}

// ChunkByIdWithCodec 使用显式排序规则消费文本、二进制或自定义主键，关联遵循 With 声明。
// 操作需要整体原子性时，调用方应绑定事务并将本函数返回的错误交给事务边界。
func ChunkByIdWithCodec[T any](query *ModelQuery, count int, pkField string, codec CursorCodec, callback func([]T) (bool, error)) error {
	scanner, count, err := prepareTypedChunk(query, count, callback)
	if err != nil {
		return err
	}
	var after any
	for {
		page, err := scanner.seek(query, count, pkField, codec, after)
		if err != nil {
			return err
		}
		if len(page.List) == 0 {
			return nil
		}
		proceed, err := callback(page.List)
		if err != nil {
			return err
		}
		if err := scanner.ctx.Err(); err != nil {
			return err
		}
		if !proceed || !page.HasMore {
			return nil
		}
		after = page.NextCursor
	}
}

func prepareTypedChunk[T any](query *ModelQuery, count int, callback func([]T) (bool, error)) (typedModelScanner[T], int, error) {
	if callback == nil {
		return typedModelScanner[T]{}, 0, fmt.Errorf("%w: 分批处理回调不能为空", ErrInvalidQuery)
	}
	if count <= 0 {
		count = DefaultChunkSize
	}
	if count >= maxQueryResultRows {
		return typedModelScanner[T]{}, 0, fmt.Errorf("%w: 分批大小超过 %d", ErrInvalidPagination, maxQueryResultRows-1)
	}
	scanner, err := newTypedModelScanner[T](query)
	return scanner, count, err
}
