package db

import (
	"fmt"
	"time"
)

// ModelQuery 封装 Model 层的链式查询，保留获取器/修改器/事件/软删除能力。
type ModelQuery struct {
	model       *Model
	query       *Query
	relations   []string
	withTrashed bool // 从 Model 移到 ModelQuery，使查询状态独立
	onlyTrashed bool // 从 Model 移到 ModelQuery
}

// newModelQuery 创建一个新的模型查询对象。
func (m *Model) newModelQuery() *ModelQuery {
	q := m.db.Name(m.table)
	q.autoTimestamp = m.autoTimestamp
	q.createTimeField = m.createTimeField
	q.updateTimeField = m.updateTimeField
	q.timestampValueType = m.timestampValueType
	return &ModelQuery{
		model: m,
		query: q,
	}
}

// clone 克隆当前的 ModelQuery 对象，用于并发安全或多次执行。
func (mq *ModelQuery) clone() *ModelQuery {
	return &ModelQuery{
		model:       mq.model,
		query:       mq.query.clone(),
		relations:   append([]string(nil), mq.relations...),
		withTrashed: mq.withTrashed,
		onlyTrashed: mq.onlyTrashed,
	}
}

// prepareQuery 准备最终执行的 Query 对象，注入软删除等限制。
func (mq *ModelQuery) prepareQuery() *Query {
	q := mq.query
	if mq.model.softDelete && !mq.withTrashed {
		if mq.onlyTrashed {
			q = q.Where(mq.model.deleteTimeField + " IS NOT NULL")
		} else {
			q = q.Where(mq.model.deleteTimeField + " IS NULL")
		}
	}
	return q
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
	if len(mq.model.getters) > 0 {
		for _, row := range rows {
			mq.model.applyGetters(row)
		}
	}
	return rows, nil
}

// Count 统计记录总数。
func (mq *ModelQuery) Count() (int64, error) {
	return mq.prepareQuery().Count()
}

// Insert 使用 map 插入记录，应用修改器并触发插入事件。
func (mq *ModelQuery) Insert(data map[string]interface{}) (int64, error) {
	mq.model.applySetters(data)
	if !mq.model.fireEvent(ModelBeforeInsert, data) {
		return 0, fmt.Errorf("before_insert 事件回调阻止了插入操作")
	}
	q := mq.prepareQuery()
	id, err := q.Insert(data)
	if err != nil {
		return 0, err
	}
	data["id"] = id
	mq.model.fireEvent(ModelAfterInsert, data)
	return id, nil
}

// Update 使用 map 更新记录，应用修改器并触发更新事件。
func (mq *ModelQuery) Update(data map[string]interface{}) (int64, error) {
	mq.model.applySetters(data)
	if !mq.model.fireEvent(ModelBeforeUpdate, data) {
		return 0, fmt.Errorf("before_update 事件回调阻止了更新操作")
	}
	q := mq.prepareQuery()
	affected, err := q.Update(data)
	if err != nil {
		return 0, err
	}
	mq.model.fireEvent(ModelAfterUpdate, data)
	return affected, nil
}

// Delete 删除记录，如果是软删除模型会自动改写为更新时间戳，触发删除事件。
func (mq *ModelQuery) Delete() (int64, error) {
	emptyData := make(map[string]interface{})
	if !mq.model.fireEvent(ModelBeforeDelete, emptyData) {
		return 0, fmt.Errorf("before_delete 事件回调阻止了删除操作")
	}
	q := mq.prepareQuery()
	var affected int64
	var err error
	if mq.model.softDelete {
		data := map[string]interface{}{}
		mq.model.setTimestamp(data, mq.model.deleteTimeField, time.Now())
		affected, err = q.Update(data)
	} else {
		affected, err = q.Delete()
	}
	if err != nil {
		return 0, err
	}
	mq.model.fireEvent(ModelAfterDelete, emptyData)
	return affected, nil
}

// ForceDelete 强制物理删除记录，忽略软删除配置，但触发删除事件。
func (mq *ModelQuery) ForceDelete() (int64, error) {
	emptyData := make(map[string]interface{})
	if !mq.model.fireEvent(ModelBeforeDelete, emptyData) {
		return 0, fmt.Errorf("before_delete 事件回调阻止了物理删除操作")
	}
	q := mq.query
	if len(q.where) == 0 {
		return 0, fmt.Errorf("ForceDelete 禁止无 WHERE 条件执行，防止误删全表数据")
	}
	affected, err := q.Delete()
	if err != nil {
		return 0, err
	}
	mq.model.fireEvent(ModelAfterDelete, emptyData)
	return affected, nil
}

// Restore 恢复软删除记录。
func (mq *ModelQuery) Restore() (int64, error) {
	if !mq.model.softDelete {
		return 0, nil
	}
	q := mq.query
	if len(q.where) == 0 {
		return 0, fmt.Errorf("Restore 禁止无 WHERE 条件执行，防止恢复全部已删除记录")
	}
	q.Where(mq.model.deleteTimeField + " IS NOT NULL")
	affected, err := q.Update(map[string]interface{}{mq.model.deleteTimeField: nil})
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
	dataMq.query.Page(page, pageSize)
	list, err := dataMq.Select()
	if err != nil {
		return nil, err
	}
	return buildPaginator(list, total, page, pageSize), nil
}

// Chunk 分块查询，每次查询 count 条记录并执行回调。
func (mq *ModelQuery) Chunk(count int, callback func(rows []map[string]interface{}) bool) error {
	if count <= 0 {
		count = 100
	}
	page := 1
	for {
		cloned := mq.clone()
		cloned.query.Page(page, count)
		rows, err := cloned.Select()
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		if !callback(rows) {
			break
		}
		if len(rows) < count {
			break
		}
		page++
	}
	return nil
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
	if getter, ok := mq.model.getters[field]; ok {
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
	getter, hasGetter := mq.model.getters[field]
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
	for _, data := range dataList {
		mq.model.applySetters(data)
	}
	return mq.prepareQuery().InsertAll(dataList)
}

// ==========================================
// 链式构建代理方法 (全部返回 *ModelQuery)
// ==========================================

// Where 添加查询条件。
func (mq *ModelQuery) Where(condition interface{}, args ...interface{}) *ModelQuery {
	mq.query.Where(condition, args...)
	return mq
}

// WhereOr 添加 OR 查询条件。
func (mq *ModelQuery) WhereOr(condition interface{}, args ...interface{}) *ModelQuery {
	mq.query.WhereOr(condition, args...)
	return mq
}

// WhereColumn 比较两个字段。
func (mq *ModelQuery) WhereColumn(left string, op string, right string) *ModelQuery {
	mq.query.WhereColumn(left, op, right)
	return mq
}

// WhereExp 使用表达式查询。
func (mq *ModelQuery) WhereExp(field string, op string, expression string, args ...interface{}) *ModelQuery {
	mq.query.WhereExp(field, op, expression, args...)
	return mq
}

// WhereTime 时间区间查询。
func (mq *ModelQuery) WhereTime(field string, operator string, values ...interface{}) *ModelQuery {
	mq.query.WhereTime(field, operator, values...)
	return mq
}

// WhereField 指定字段的查询条件。
func (mq *ModelQuery) WhereField(field string, op string, value interface{}) *ModelQuery {
	mq.query.WhereField(field, op, value)
	return mq
}

// WhereMap 使用 map 传入多个查询条件。
func (mq *ModelQuery) WhereMap(conditions map[string]interface{}) *ModelQuery {
	mq.query.WhereMap(conditions)
	return mq
}

// WhereFields 批量指定字段的查询条件。
func (mq *ModelQuery) WhereFields(conditions [][]interface{}) *ModelQuery {
	mq.query.WhereFields(conditions)
	return mq
}

// WhereIn 字段值在指定列表中。
func (mq *ModelQuery) WhereIn(field string, values []interface{}) *ModelQuery {
	mq.query.WhereIn(field, values)
	return mq
}

// WhereNotIn 字段值不在指定列表中。
func (mq *ModelQuery) WhereNotIn(field string, values []interface{}) *ModelQuery {
	mq.query.WhereNotIn(field, values)
	return mq
}

// WhereLike 模糊查询。
func (mq *ModelQuery) WhereLike(field string, pattern string) *ModelQuery {
	mq.query.WhereLike(field, pattern)
	return mq
}

// WhereNull 字段值为 NULL。
func (mq *ModelQuery) WhereNull(field string) *ModelQuery {
	mq.query.WhereNull(field)
	return mq
}

// WhereNotNull 字段值不为 NULL。
func (mq *ModelQuery) WhereNotNull(field string) *ModelQuery {
	mq.query.WhereNotNull(field)
	return mq
}

// WhereBetween 字段值在指定区间。
func (mq *ModelQuery) WhereBetween(field string, min, max interface{}) *ModelQuery {
	mq.query.WhereBetween(field, min, max)
	return mq
}

// WhereRaw 原生 SQL 查询条件。
func (mq *ModelQuery) WhereRaw(rawSQL string, args ...interface{}) *ModelQuery {
	mq.query.WhereRaw(rawSQL, args...)
	return mq
}

// Limit 限制查询结果数量。
func (mq *ModelQuery) Limit(limit int) *ModelQuery {
	mq.query.Limit(limit)
	return mq
}

// Offset 设置查询偏移量。
func (mq *ModelQuery) Offset(offset int) *ModelQuery {
	mq.query.Offset(offset)
	return mq
}

// Page 设置分页参数。
func (mq *ModelQuery) Page(page, pageSize int) *ModelQuery {
	mq.query.Page(page, pageSize)
	return mq
}

// Order 设置排序规则。
func (mq *ModelQuery) Order(order string) *ModelQuery {
	mq.query.Order(order)
	return mq
}

// Field 设置查询的字段列表。
func (mq *ModelQuery) Field(fields string) *ModelQuery {
	mq.query.Field(fields)
	return mq
}

// Group 设置分组。
func (mq *ModelQuery) Group(group string) *ModelQuery {
	mq.query.Group(group)
	return mq
}

// Having 设置 Having 条件。
func (mq *ModelQuery) Having(having string, args ...interface{}) *ModelQuery {
	mq.query.Having(having, args...)
	return mq
}

// HavingRaw 设置原生 Having 条件。
func (mq *ModelQuery) HavingRaw(having string, args ...interface{}) *ModelQuery {
	mq.query.HavingRaw(having, args...)
	return mq
}

// HavingField 指定字段的 Having 条件。
func (mq *ModelQuery) HavingField(field string, op string, value interface{}) *ModelQuery {
	mq.query.HavingField(field, op, value)
	return mq
}

// Distinct 去除重复记录。
func (mq *ModelQuery) Distinct() *ModelQuery {
	mq.query.Distinct()
	return mq
}

// Join 连接查询。
func (mq *ModelQuery) Join(table, condition string) *ModelQuery {
	mq.query.Join(table, condition)
	return mq
}

// LeftJoin 左外连接查询。
func (mq *ModelQuery) LeftJoin(table, condition string) *ModelQuery {
	mq.query.LeftJoin(table, condition)
	return mq
}

// RightJoin 右外连接查询。
func (mq *ModelQuery) RightJoin(table, condition string) *ModelQuery {
	mq.query.RightJoin(table, condition)
	return mq
}

// Inc 字段自增。
func (mq *ModelQuery) Inc(field string, step ...int) *ModelQuery {
	mq.query.Inc(field, step...)
	return mq
}

// Dec 字段自减。
func (mq *ModelQuery) Dec(field string, step ...int) *ModelQuery {
	mq.query.Dec(field, step...)
	return mq
}

// Lock 锁机制。
func (mq *ModelQuery) Lock(exclusive ...bool) *ModelQuery {
	mq.query.Lock(exclusive...)
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
	for _, relationName := range mq.relations {
		relation, ok := mq.model.relations[relationName]
		if !ok {
			return fmt.Errorf("relation not defined: %s", relationName)
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
		keys := collectRelationKeys(rows, relation.LocalKey)
		if len(keys) == 0 {
			return nil
		}
		relatedRows, err := relation.Related.query().WhereIn(relation.ForeignKey, keys).Select()
		if err != nil {
			return err
		}
		grouped := groupRelationRows(relatedRows, relation.ForeignKey)
		for _, row := range rows {
			key := fmt.Sprint(row[relation.LocalKey])
			if relation.Type == relationHasOne {
				records := grouped[key]
				if len(records) > 0 {
					row[relation.Name] = records[0]
				} else {
					row[relation.Name] = map[string]interface{}(nil)
				}
				continue
			}
			row[relation.Name] = grouped[key]
		}
	case relationBelongsTo:
		keys := collectRelationKeys(rows, relation.ForeignKey)
		if len(keys) == 0 {
			return nil
		}
		relatedRows, err := relation.Related.query().WhereIn(relation.OwnerKey, keys).Select()
		if err != nil {
			return err
		}
		indexed := indexRelationRows(relatedRows, relation.OwnerKey)
		for _, row := range rows {
			row[relation.Name] = indexed[fmt.Sprint(row[relation.ForeignKey])]
		}
	case relationBelongsToMany:
		keys := collectRelationKeys(rows, relation.LocalKey)
		if len(keys) == 0 {
			return nil
		}
		pivotRows, err := mq.model.db.Name(relation.PivotTable).WhereIn(relation.ForeignKey, keys).Select()
		if err != nil {
			return fmt.Errorf("预加载多对多关联 %s 中间表查询失败: %w", relation.Name, err)
		}
		if len(pivotRows) == 0 {
			for _, row := range rows {
				row[relation.Name] = []map[string]interface{}{}
			}
			return nil
		}
		pivotMap := make(map[string][]interface{})
		relatedKeySet := make(map[string]bool)
		allRelatedKeys := make([]interface{}, 0)
		for _, pRow := range pivotRows {
			lk := fmt.Sprint(pRow[relation.ForeignKey])
			rk := pRow[relation.RelatedForeignKey]
			pivotMap[lk] = append(pivotMap[lk], rk)
			rkStr := fmt.Sprint(rk)
			if !relatedKeySet[rkStr] {
				relatedKeySet[rkStr] = true
				allRelatedKeys = append(allRelatedKeys, rk)
			}
		}
		relatedRows, err := relation.Related.query().WhereIn("id", allRelatedKeys).Select()
		if err != nil {
			return err
		}
		relatedIndex := indexRelationRows(relatedRows, "id")
		for _, row := range rows {
			lk := fmt.Sprint(row[relation.LocalKey])
			relatedIds := pivotMap[lk]
			related := make([]map[string]interface{}, 0, len(relatedIds))
			for _, rid := range relatedIds {
				if r, ok := relatedIndex[fmt.Sprint(rid)]; ok {
					related = append(related, r)
				}
			}
			row[relation.Name] = related
		}
	}
	return nil
}
