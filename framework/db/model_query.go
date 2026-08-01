package db

import (
	"context"
	"fmt"
)

// ModelQuery 封装 Model 层的链式查询，保留获取器/修改器/事件/软删除能力。
type ModelQuery struct {
	model       *Model
	query       *Query
	relations   []string
	withTrashed bool // 从 Model 移到 ModelQuery，使查询状态独立
	onlyTrashed bool // 从 Model 移到 ModelQuery
}

func (mq *ModelQuery) validationError() error {
	if mq == nil || mq.model == nil || mq.query == nil {
		return fmt.Errorf("%w: 模型查询不能为空", ErrInvalidModel)
	}
	return mq.model.validationError()
}

// newModelQuery 创建一个新的模型查询对象。
func (m *Model) newModelQuery() *ModelQuery {
	if m == nil {
		return &ModelQuery{query: newQuery(nil, "").setError(ErrInvalidModel)}
	}
	m.mu.RLock()
	database := m.db
	table := m.table
	autoTimestamp := m.autoTimestamp
	createTimeField := m.createTimeField
	updateTimeField := m.updateTimeField
	timestampValueType := m.timestampValueType
	primaryKey := m.primaryKey
	primaryKeyExplicit := m.primaryKeyExplicit
	configErr := m.configErr
	m.mu.RUnlock()
	q := newQuery(database, table)
	if configErr != nil {
		q.setError(configErr)
	}
	q.autoTimestamp = autoTimestamp
	q.createTimeField = createTimeField
	q.updateTimeField = updateTimeField
	q.timestampValueType = timestampValueType
	if primaryKey == "" {
		primaryKey = "id"
	}
	storageKey := primaryKey
	if resolved, err := resolveModelStoragePrimaryKey(database, primaryKey, primaryKeyExplicit); err != nil {
		q.setError(err)
	} else {
		storageKey = resolved
		q.insertPrimaryKey = storageKey
		q.modelPrimaryKey = primaryKey
	}
	return &ModelQuery{
		model: m,
		query: q,
	}
}

// clone 复制 ModelQuery 外层状态，并共享不可变的 Query 快照。
// Query 的公开变更方法会自行复制，因此代理链只需要复制一次。
func (mq *ModelQuery) clone() *ModelQuery {
	if mq == nil {
		return nil
	}
	return &ModelQuery{
		model:       mq.model,
		query:       mq.query,
		relations:   append([]string(nil), mq.relations...),
		withTrashed: mq.withTrashed,
		onlyTrashed: mq.onlyTrashed,
	}
}

// prepareQuery 准备最终执行的 Query 对象，注入软删除等限制。
// 在克隆上追加软删除条件，避免污染共享的 mq.query —— 否则对同一个 ModelQuery
// 先后调用 Count()/Select() 会重复追加 "delete_time IS NULL"，且使终端方法不可重复执行。
func (mq *ModelQuery) prepareQuery() *Query {
	if mq == nil || mq.query == nil || mq.model == nil {
		return newQuery(nil, "").setError(ErrInvalidModel)
	}
	q := mq.query.clone()
	mq.model.mu.RLock()
	softDelete := mq.model.softDelete
	deleteTimeField := mq.model.deleteTimeField
	mq.model.mu.RUnlock()
	if softDelete && !mq.withTrashed {
		if mq.onlyTrashed {
			q = q.Where(deleteTimeField + " IS NOT NULL")
		} else {
			q = q.Where(deleteTimeField + " IS NULL")
		}
	}
	return q
}

// relationModelQuery 创建关联查询，并继承根查询的请求上下文。
// 关联查询使用全新的 Query，因此可以直接写入上下文，避免额外复制。
func (mq *ModelQuery) relationModelQuery(model *Model) *ModelQuery {
	related := model.newModelQuery()
	if related != nil && related.query != nil && mq != nil && mq.query != nil && mq.query.ctx != nil {
		related.query.ctx = mq.query.ctx
	}
	return related
}

// relationQueryContext 将根查询上下文绑定到新创建的原生查询。
// 该方法只接收尚未对外暴露的查询副本，调用方后续仍通过不可变链式方法派生。
func (mq *ModelQuery) relationQueryContext(query *Query) *Query {
	if query != nil && mq != nil && mq.query != nil && mq.query.ctx != nil {
		query.ctx = mq.query.ctx
	}
	return query
}

// PrepareQuery 准备并获取最终执行的 Query 对象，供外部及测试访问。
func (mq *ModelQuery) PrepareQuery() *Query {
	return mq.prepareQuery()
}

// ==========================================
// 终端执行方法 (带 Model 层能力)
// ==========================================

// Find 查询单条记录，自动应用获取器并预加载关联。
func (mq *ModelQuery) Find() (map[string]interface{}, error) {
	q := mq.prepareQuery()
	row, err := q.Find()
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	if len(mq.relations) > 0 {
		rows := []map[string]interface{}{row}
		if err := mq.loadRelations(rows); err != nil {
			return nil, err
		}
		row = rows[0]
	}
	return mq.model.applyGetters(row), nil
}

// Select 查询多条记录，自动应用获取器并预加载关联。
func (mq *ModelQuery) Select() ([]map[string]interface{}, error) {
	q := mq.prepareQuery()
	rows, err := q.Select()
	if err != nil {
		return nil, err
	}
	if len(mq.relations) > 0 {
		if err := mq.loadRelations(rows); err != nil {
			return nil, err
		}
	}
	for _, row := range rows {
		mq.model.applyGetters(row)
	}
	return rows, nil
}

// Each 按行消费模型查询结果，避免把大结果集长期物化到内存。
// 预加载关联会引入额外批量查询，流式路径明确拒绝该组合，避免隐藏 N+1 查询。
func (mq *ModelQuery) Each(callback func(row map[string]interface{}) bool) error {
	if err := mq.validationError(); err != nil {
		return err
	}
	if callback == nil {
		return fmt.Errorf("%w: 模型 Each 回调不能为空", ErrInvalidQuery)
	}
	if len(mq.relations) > 0 {
		return fmt.Errorf("%w: ModelQuery Each 不支持关联预加载，请使用 Select 或 Chunk", ErrUnsupportedFeature)
	}
	return mq.prepareQuery().Each(func(row map[string]interface{}) bool {
		mq.model.applyGetters(row)
		return callback(row)
	})
}

// Count 统计记录总数。
func (mq *ModelQuery) Count() (int64, error) {
	return mq.prepareQuery().Count()
}

// Insert 使用 map 插入记录并返回影响行数，语义与 Query.Insert/ThinkPHP 一致。
func (mq *ModelQuery) Insert(data map[string]interface{}) (int64, error) {
	result, err := mq.insertResult(data, false)
	if err != nil {
		return 0, err
	}
	return result.Affected, nil
}

// InsertGetId 使用 map 插入记录并返回驱动报告的真实主键。
func (mq *ModelQuery) InsertGetId(data map[string]interface{}) (interface{}, error) {
	result, err := mq.insertResult(data, true)
	if err != nil {
		return nil, err
	}
	return result.InsertedID()
}

func (mq *ModelQuery) insertResult(data map[string]interface{}, wantID bool) (InsertResult, error) {
	if err := mq.validationError(); err != nil {
		return InsertResult{}, err
	}
	workingData := cloneDatabaseMap(data)
	mq.model.applySetters(workingData)
	var allowed bool
	workingData, allowed = mq.model.dispatchBeforeEvent(ModelBeforeInsert, workingData)
	if !allowed {
		return InsertResult{}, fmt.Errorf("before_insert 事件回调阻止了插入操作")
	}
	q := mq.prepareQuery()
	result, err := q.insertResult(workingData, wantID)
	if err != nil {
		return InsertResult{}, err
	}
	persistedData := result.Data
	if persistedData == nil {
		persistedData = cloneDatabaseMap(workingData)
	} else {
		persistedData = cloneDatabaseMap(persistedData)
	}
	if wantID {
		id, idErr := result.InsertedID()
		if idErr != nil {
			return InsertResult{}, idErr
		}
		persistedData[mq.model.primaryKeyField()] = id
	}
	result.Data = persistedData
	mq.model.dispatchAfterEvent(ModelAfterInsert, persistedData)
	return result, nil
}

// Update 使用 map 更新记录，应用修改器并触发更新事件。
func (mq *ModelQuery) Update(data map[string]interface{}) (int64, error) {
	if err := mq.validationError(); err != nil {
		return 0, err
	}
	// 软删除模型会自动追加 delete_time 条件；该框架条件不能替代调用方业务条件，
	// 否则一次无条件更新会覆盖全部未删除记录。
	if len(mq.query.where) == 0 {
		return 0, ErrUnsafeFullTableMutation
	}
	workingData := cloneDatabaseMap(data)
	mq.model.applySetters(workingData)
	var allowed bool
	workingData, allowed = mq.model.dispatchBeforeEvent(ModelBeforeUpdate, workingData)
	if !allowed {
		return 0, fmt.Errorf("before_update 事件回调阻止了更新操作")
	}
	q := mq.prepareQuery()
	result, err := q.UpdateResult(workingData)
	if err != nil {
		return 0, err
	}
	mq.model.dispatchAfterEvent(ModelAfterUpdate, result.Data)
	return result.Count(), nil
}

// Delete 删除记录，如果是软删除模型会自动改写为更新时间戳，触发删除事件。
func (mq *ModelQuery) Delete() (int64, error) {
	if err := mq.validationError(); err != nil {
		return 0, err
	}
	if len(mq.query.where) == 0 {
		return 0, ErrUnsafeFullTableMutation
	}
	beforeData, allowed := mq.model.dispatchBeforeEvent(ModelBeforeDelete, map[string]interface{}{})
	if !allowed {
		return 0, fmt.Errorf("before_delete 事件回调阻止了删除操作")
	}
	q := mq.prepareQuery()
	var affected int64
	var afterData map[string]interface{}
	var err error
	mq.model.mu.RLock()
	softDelete := mq.model.softDelete
	deleteTimeField := mq.model.deleteTimeField
	mq.model.mu.RUnlock()
	if softDelete {
		data := map[string]interface{}{}
		if err := mq.model.setTimestamp(data, deleteTimeField, q.now()); err != nil {
			return 0, err
		}
		var result UpdateResult
		result, err = q.UpdateResult(data)
		if err == nil {
			affected = result.Count()
			afterData = updateEventResultData(result)
		}
	} else {
		var result DeleteResult
		result, err = q.DeleteResult()
		if err == nil {
			affected = result.Deleted
			afterData = deleteEventResultData(beforeData, result)
		}
	}
	if err != nil {
		return 0, err
	}
	mq.model.dispatchAfterEvent(ModelAfterDelete, afterData)
	return affected, nil
}

// ForceDelete 强制物理删除记录，忽略软删除配置，但触发删除事件。
func (mq *ModelQuery) ForceDelete() (int64, error) {
	if err := mq.validationError(); err != nil {
		return 0, err
	}
	if len(mq.query.where) == 0 {
		return 0, ErrUnsafeFullTableMutation
	}
	beforeData, allowed := mq.model.dispatchBeforeEvent(ModelBeforeDelete, map[string]interface{}{})
	if !allowed {
		return 0, fmt.Errorf("before_delete 事件回调阻止了物理删除操作")
	}
	q := mq.query.clone()
	if len(q.where) == 0 {
		return 0, ErrUnsafeFullTableMutation
	}
	result, err := q.DeleteResult()
	if err != nil {
		return 0, err
	}
	mq.model.dispatchAfterEvent(ModelAfterDelete, deleteEventResultData(beforeData, result))
	return result.Deleted, nil
}

// Restore 恢复软删除记录。
func (mq *ModelQuery) Restore() (int64, error) {
	if err := mq.validationError(); err != nil {
		return 0, err
	}
	mq.model.mu.RLock()
	softDelete := mq.model.softDelete
	deleteTimeField := mq.model.deleteTimeField
	mq.model.mu.RUnlock()
	if !softDelete {
		return 0, fmt.Errorf("%w: Restore 仅适用于软删除模型", ErrInvalidModel)
	}
	// 恢复条件必须由业务方明确限定，自动追加的“已删除”条件不能放开全表恢复。
	if len(mq.query.where) == 0 {
		return 0, ErrUnsafeFullTableMutation
	}
	q := mq.query.clone()
	q = q.Where(deleteTimeField + " IS NOT NULL")
	affected, err := q.Update(map[string]interface{}{deleteTimeField: nil})
	return affected, err
}

// Paginate 执行带总数统计的分页查询，自动应用获取器并预加载。
func (mq *ModelQuery) Paginate(page, pageSize int) (*Paginator, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	countMq := mq.clone()
	total, err := countMq.Count()
	if err != nil {
		return nil, err
	}
	dataMq := mq.clone()
	dataMq.query = dataMq.query.Page(page, pageSize)
	list, err := dataMq.Select()
	if err != nil {
		return nil, err
	}
	return buildPaginator(list, total, page, pageSize)
}

// Chunk 分块查询，每次查询 count 条记录并执行回调。
func (mq *ModelQuery) Chunk(count int, callback func(rows []map[string]interface{}) bool) error {
	if count <= 0 {
		count = 100
	}
	if callback == nil {
		return fmt.Errorf("%w: 模型 Chunk 回调不能为空", ErrInvalidQuery)
	}
	readLimit := count
	hasLookAhead := mq.query.canStreamSQLRows()
	if hasLookAhead {
		readLimit, hasLookAhead = chunkReadLimit(count)
	}
	page := 1
	for {
		cloned := mq.clone()
		cloned.query = cloned.query.Page(page, count)
		cloned.query.limit = readLimit
		rows, err := cloned.Select()
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		hasMore := hasLookAhead && len(rows) > count
		visibleRows := rows
		if hasMore {
			visibleRows = rows[:count]
		}
		if !callback(visibleRows) {
			break
		}
		if len(visibleRows) < count {
			break
		}
		if hasLookAhead && !hasMore {
			break
		}
		page++
	}
	return nil
}

// ChunkById 基于主键游标分批遍历（避免 OFFSET 深分页问题），自动应用软删除限制与获取器。
func (mq *ModelQuery) ChunkById(count int, pkField string, callback func(rows []map[string]interface{}) bool) error {
	return mq.ChunkByIdWithCodec(count, pkField, OrderedCursorCodec{}, callback)
}

// ChunkByIdWithCodec 使用显式数据库排序 codec 遍历文本、二进制或自定义主键。
func (mq *ModelQuery) ChunkByIdWithCodec(count int, pkField string, codec CursorCodec, callback func(rows []map[string]interface{}) bool) error {
	if err := mq.validationError(); err != nil {
		return err
	}
	if callback == nil {
		return fmt.Errorf("%w: 模型 ChunkById 回调不能为空", ErrInvalidQuery)
	}
	if pkField == "" {
		pkField = mq.model.primaryKeyField()
	}
	return mq.prepareQuery().ChunkByIdWithCodec(count, pkField, codec, func(rows []map[string]interface{}) bool {
		for _, row := range rows {
			mq.model.applyGetters(row)
		}
		return callback(rows)
	})
}

// SeekPage 使用模型主键游标读取一页数据，不执行 COUNT 或 OFFSET，并应用模型获取器。
// 该方法与 ChunkById 一样不执行 With 关联预加载，避免游标分页隐藏额外查询。
func (mq *ModelQuery) SeekPage(pageSize int, pkField string, after interface{}) (*CursorPage, error) {
	return mq.SeekPageWithCodec(pageSize, pkField, OrderedCursorCodec{}, after)
}

// SeekPageWithCodec 使用显式排序 codec 执行模型游标分页，并应用模型获取器。
func (mq *ModelQuery) SeekPageWithCodec(pageSize int, pkField string, codec CursorCodec, after interface{}) (*CursorPage, error) {
	if err := mq.validationError(); err != nil {
		return nil, err
	}
	if pkField == "" {
		pkField = mq.model.primaryKeyField()
	}
	page, err := mq.prepareQuery().SeekPageWithCodec(pageSize, pkField, codec, after)
	if err != nil {
		return nil, err
	}
	for _, row := range page.List {
		mq.model.applyGetters(row)
	}
	return page, nil
}

// Sum 统计字段总和。
func (mq *ModelQuery) Sum(field string) (float64, error) {
	return mq.prepareQuery().Sum(field)
}

// Avg 统计字段平均值。
func (mq *ModelQuery) Avg(field string) (float64, error) {
	return mq.prepareQuery().Avg(field)
}

// Max 统计字段最大值。
func (mq *ModelQuery) Max(field string) (float64, error) {
	return mq.prepareQuery().Max(field)
}

// Min 统计字段最小值。
func (mq *ModelQuery) Min(field string) (float64, error) {
	return mq.prepareQuery().Min(field)
}

// Value 获取单条记录的单个字段值。
func (mq *ModelQuery) Value(field string) (interface{}, error) {
	q := mq.prepareQuery()
	val, err := q.Value(field)
	if err != nil {
		return nil, err
	}
	if val == nil {
		return nil, nil
	}
	mq.model.mu.RLock()
	getter, ok := mq.model.getters[field]
	mq.model.mu.RUnlock()
	if ok {
		return getter(val, map[string]interface{}{field: val}), nil
	}
	return val, nil
}

// Column 获取某个字段的所有值列表。
func (mq *ModelQuery) Column(field string, key ...string) (interface{}, error) {
	q := mq.prepareQuery()
	res, err := q.Column(field, key...)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	mq.model.mu.RLock()
	getter, hasGetter := mq.model.getters[field]
	mq.model.mu.RUnlock()
	if !hasGetter {
		return res, nil
	}
	switch val := res.(type) {
	case []interface{}:
		for i, v := range val {
			val[i] = getter(v, map[string]interface{}{field: v})
		}
		return val, nil
	case map[string]interface{}:
		for k, v := range val {
			var mockRow map[string]interface{}
			if len(key) > 0 && key[0] != "" {
				mockRow = map[string]interface{}{field: v, key[0]: k}
			} else {
				mockRow = map[string]interface{}{field: v}
			}
			val[k] = getter(v, mockRow)
		}
		return val, nil
	}
	return res, nil
}

// InsertAll 批量插入多条记录。
func (mq *ModelQuery) InsertAll(dataList []map[string]interface{}) (int64, error) {
	workingList := make([]map[string]interface{}, len(dataList))
	for index, data := range dataList {
		workingList[index] = cloneDatabaseMap(data)
	}
	for _, data := range workingList {
		mq.model.applySetters(data)
	}
	return mq.prepareQuery().InsertAll(workingList)
}

// ==========================================
// 链式构建代理方法 (全部返回 *ModelQuery)
// ==========================================

// WithContext 绑定查询上下文，使底层 SQL 执行可随请求超时/取消。
func (mq *ModelQuery) WithContext(ctx context.Context) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WithContext(ctx)
	return mq
}

// Where 添加查询条件。
func (mq *ModelQuery) Where(condition interface{}, args ...interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.Where(condition, args...)
	return mq
}

// WhereOr 添加 OR 查询条件。
func (mq *ModelQuery) WhereOr(condition interface{}, args ...interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereOr(condition, args...)
	return mq
}

// WhereColumn 比较两个字段。
func (mq *ModelQuery) WhereColumn(left string, op string, right string) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereColumn(left, op, right)
	return mq
}

// WhereExp 使用表达式查询。
func (mq *ModelQuery) WhereExp(field string, op string, expression string, args ...interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereExp(field, op, expression, args...)
	return mq
}

// WhereTime 时间区间查询。
func (mq *ModelQuery) WhereTime(field string, operator string, values ...interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereTime(field, operator, values...)
	return mq
}

// WhereTimeAs 按指定存储类型添加模型时间条件。
func (mq *ModelQuery) WhereTimeAs(field string, valueType string, operator string, values ...interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereTimeAs(field, valueType, operator, values...)
	return mq
}

// WhereField 指定字段的查询条件。
func (mq *ModelQuery) WhereField(field string, op string, value interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereField(field, op, value)
	return mq
}

// WhereMap 使用 map 传入多个查询条件。
func (mq *ModelQuery) WhereMap(conditions map[string]interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereMap(conditions)
	return mq
}

// WhereFields 批量指定字段的查询条件。
func (mq *ModelQuery) WhereFields(conditions [][]interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereFields(conditions)
	return mq
}

// WhereIn 字段值在指定列表中。
func (mq *ModelQuery) WhereIn(field string, values []interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereIn(field, values)
	return mq
}

// WhereNotIn 字段值不在指定列表中。
func (mq *ModelQuery) WhereNotIn(field string, values []interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereNotIn(field, values)
	return mq
}

// WhereLike 模糊查询。
func (mq *ModelQuery) WhereLike(field string, pattern string) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereLike(field, pattern)
	return mq
}

// WhereNull 字段值为 NULL。
func (mq *ModelQuery) WhereNull(field string) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereNull(field)
	return mq
}

// WhereNotNull 字段值不为 NULL。
func (mq *ModelQuery) WhereNotNull(field string) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereNotNull(field)
	return mq
}

// WhereBetween 字段值在指定区间。
func (mq *ModelQuery) WhereBetween(field string, min, max interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereBetween(field, min, max)
	return mq
}

// WhereRaw 原生 SQL 查询条件。
func (mq *ModelQuery) WhereRaw(rawSQL string, args ...interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereRaw(rawSQL, args...)
	return mq
}

// Limit 限制查询结果数量。
func (mq *ModelQuery) Limit(limit int) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.Limit(limit)
	return mq
}

// Offset 设置查询偏移量。
func (mq *ModelQuery) Offset(offset int) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.Offset(offset)
	return mq
}

// Page 设置分页参数。
func (mq *ModelQuery) Page(page, pageSize int) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.Page(page, pageSize)
	return mq
}

// Order 设置排序规则。
func (mq *ModelQuery) Order(order string) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.Order(order)
	return mq
}

// Field 设置查询的字段列表。
func (mq *ModelQuery) Field(fields string) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.Field(fields)
	return mq
}

// Group 设置分组。
func (mq *ModelQuery) Group(group string) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.Group(group)
	return mq
}

// Having 设置 Having 条件。
func (mq *ModelQuery) Having(having string, args ...interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.Having(having, args...)
	return mq
}

// HavingRaw 设置原生 Having 条件。
func (mq *ModelQuery) HavingRaw(having string, args ...interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.HavingRaw(having, args...)
	return mq
}

// HavingField 指定字段的 Having 条件。
func (mq *ModelQuery) HavingField(field string, op string, value interface{}) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.HavingField(field, op, value)
	return mq
}

// Distinct 去除重复记录。
func (mq *ModelQuery) Distinct() *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.Distinct()
	return mq
}

// Join 连接查询。
func (mq *ModelQuery) Join(table, condition string) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.Join(table, condition)
	return mq
}

// LeftJoin 左外连接查询。
func (mq *ModelQuery) LeftJoin(table, condition string) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.LeftJoin(table, condition)
	return mq
}

// RightJoin 右外连接查询。
func (mq *ModelQuery) RightJoin(table, condition string) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.RightJoin(table, condition)
	return mq
}

// Inc 字段自增。
func (mq *ModelQuery) Inc(field string, step ...int) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.Inc(field, step...)
	return mq
}

// Dec 字段自减。
func (mq *ModelQuery) Dec(field string, step ...int) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.Dec(field, step...)
	return mq
}

// Lock 锁机制。
func (mq *ModelQuery) Lock(exclusive ...bool) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.Lock(exclusive...)
	return mq
}

// ==========================================
// 关系预加载底层实现
// ==========================================

// loadRelations 对结果集执行关系预加载。
func (mq *ModelQuery) loadRelations(rows []map[string]interface{}) error {
	if mq == nil || mq.model == nil || len(rows) == 0 || len(mq.relations) == 0 {
		return nil
	}
	mq.model.mu.RLock()
	definitions := make(map[string]RelationDefinition, len(mq.model.relations))
	for name, definition := range mq.model.relations {
		definitions[name] = definition
	}
	mq.model.mu.RUnlock()
	loaded := make(map[string]bool, len(mq.relations))
	for _, relationName := range mq.relations {
		if loaded[relationName] {
			return fmt.Errorf("%w: 关联 %q 重复预加载", ErrInvalidRelation, relationName)
		}
		loaded[relationName] = true
		relation, ok := definitions[relationName]
		if !ok {
			return fmt.Errorf("%w: 关联 %q 未定义", ErrInvalidRelation, relationName)
		}
		if err := mq.loadRelation(rows, relation); err != nil {
			return err
		}
	}
	return nil
}

// loadRelation 预加载单个关系。
func (mq *ModelQuery) loadRelation(rows []map[string]interface{}, relation RelationDefinition) error {
	switch relation.Type {
	case relationHasOne, relationHasMany:
		keys, err := collectRelationKeys(rows, relation.LocalKey)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			return nil
		}
		relatedRows, err := selectRelationRows(mq.relationModelQuery(relation.Related).WhereIn(relation.ForeignKey, keys), relation.ForeignKey)
		if err != nil {
			return err
		}
		grouped, err := groupRawRelationRows(relatedRows, relation.ForeignKey)
		if err != nil {
			return err
		}
		for index, row := range rows {
			value, exists := row[relation.LocalKey]
			if !exists {
				return fmt.Errorf("%w: 第 %d 行缺少本地键 %q", ErrInvalidRelation, index, relation.LocalKey)
			}
			key, usable, err := relationComparableValueKey(value)
			if err != nil {
				return err
			}
			if relation.Type == relationHasOne {
				records := grouped[key]
				if len(records) > 1 {
					return fmt.Errorf("%w: 一对一关联 %q 返回多条记录", ErrInvalidRelation, relation.Name)
				}
				if usable && len(records) == 1 {
					row[relation.Name] = records[0]
				} else {
					row[relation.Name] = map[string]interface{}(nil)
				}
				continue
			}
			if !usable {
				row[relation.Name] = []map[string]interface{}{}
			} else {
				row[relation.Name] = grouped[key]
			}
		}
	case relationBelongsTo:
		keys, err := collectRelationKeys(rows, relation.ForeignKey)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			return nil
		}
		relatedRows, err := selectRelationRows(mq.relationModelQuery(relation.Related).WhereIn(relation.OwnerKey, keys), relation.OwnerKey)
		if err != nil {
			return err
		}
		indexed, err := indexRawRelationRows(relatedRows, relation.OwnerKey)
		if err != nil {
			return err
		}
		for index, row := range rows {
			value, exists := row[relation.ForeignKey]
			if !exists {
				return fmt.Errorf("%w: 第 %d 行缺少外键 %q", ErrInvalidRelation, index, relation.ForeignKey)
			}
			key, usable, err := relationComparableValueKey(value)
			if err != nil {
				return err
			}
			if usable {
				row[relation.Name] = indexed[key]
			} else {
				row[relation.Name] = map[string]interface{}(nil)
			}
		}
	case relationBelongsToMany:
		keys, err := collectRelationKeys(rows, relation.LocalKey)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			return nil
		}
		mq.model.mu.RLock()
		database := mq.model.db
		mq.model.mu.RUnlock()
		pivotQuery := mq.relationQueryContext(database.Name(relation.PivotTable)).
			Field(relation.ForeignKey+", "+relation.RelatedForeignKey).
			WhereIn(relation.ForeignKey, keys)
		pivotRows, err := pivotQuery.Select()
		if err != nil {
			return fmt.Errorf("预加载多对多关联 %s 中间表查询失败: %w", relation.Name, err)
		}
		if len(pivotRows) == 0 {
			for _, row := range rows {
				row[relation.Name] = []map[string]interface{}{}
			}
			return nil
		}
		pivotMap := make(map[relationComparableKey][]interface{})
		relatedKeySet := make(map[relationComparableKey]bool)
		allRelatedKeys := make([]interface{}, 0)
		for index, pivotRow := range pivotRows {
			localValue, localExists := pivotRow[relation.ForeignKey]
			relatedValue, relatedExists := pivotRow[relation.RelatedForeignKey]
			if !localExists || !relatedExists {
				return fmt.Errorf("%w: 中间表第 %d 行缺少关联键", ErrInvalidRelation, index)
			}
			localKey, localUsable, err := relationComparableValueKey(localValue)
			if err != nil {
				return err
			}
			relatedKey, relatedUsable, err := relationComparableValueKey(relatedValue)
			if err != nil {
				return err
			}
			if !localUsable || !relatedUsable {
				continue
			}
			rk := relatedValue
			lk := localKey
			pivotMap[lk] = append(pivotMap[lk], rk)
			if !relatedKeySet[relatedKey] {
				relatedKeySet[relatedKey] = true
				allRelatedKeys = append(allRelatedKeys, rk)
			}
		}
		relatedPK := relation.Related.primaryKeyField()
		relatedRows, err := selectRelationRows(mq.relationModelQuery(relation.Related).WhereIn(relatedPK, allRelatedKeys), relatedPK)
		if err != nil {
			return err
		}
		relatedIndex, err := indexRawRelationRows(relatedRows, relatedPK)
		if err != nil {
			return err
		}
		for index, row := range rows {
			localValue, exists := row[relation.LocalKey]
			if !exists {
				return fmt.Errorf("%w: 第 %d 行缺少本地键 %q", ErrInvalidRelation, index, relation.LocalKey)
			}
			localKey, usable, err := relationComparableValueKey(localValue)
			if err != nil {
				return err
			}
			if !usable {
				row[relation.Name] = []map[string]interface{}{}
				continue
			}
			relatedIds := pivotMap[localKey]
			related := make([]map[string]interface{}, 0, len(relatedIds))
			for _, rid := range relatedIds {
				relatedKey, _, err := relationComparableValueKey(rid)
				if err != nil {
					return err
				}
				if r, ok := relatedIndex[relatedKey]; ok {
					related = append(related, r)
				}
			}
			row[relation.Name] = related
		}
	default:
		return fmt.Errorf("%w: 未知关联类型 %q", ErrInvalidRelation, relation.Type)
	}
	return nil
}
