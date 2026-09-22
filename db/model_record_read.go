package db

import "fmt"

// Refresh 按原始主键重新加载记录，丢弃未保存的修改并重新建立字段快照。
// 回滚后可通过原非事务模型调用；仍绑定已结束事务的句柄必须重新解析模型。
func (m *Model) Refresh() error {
	if err := m.operationReady(); err != nil {
		return err
	}
	if m.record == nil {
		return fmt.Errorf("%w: 未绑定记录", ErrInvalidModel)
	}
	r := m.record
	if !r.mu.TryLock() {
		return ErrModelRecordBusy
	}
	defer r.mu.Unlock()
	if err := r.validateOwner(); err != nil {
		return err
	}
	if r.tx != nil && r.tx != m.transaction {
		r.tx.mu.Lock()
		pending := r.tx.state == transactionActive || r.tx.state == transactionCompleting
		r.tx.mu.Unlock()
		if pending {
			return ErrModelRecordTransaction
		}
	}
	if !r.persisted || isZeroDBValue(r.identity) {
		return ErrModelIdentityMissing
	}
	if m.table != r.table || m.db != r.database || m.primaryKeyField() != r.primaryKey {
		return ErrModelIdentityChanged
	}
	found, err := m.WithTrashed().Where(r.primaryKey+" = ?", r.identity).Find(r.owner.Addr().Interface())
	if err != nil {
		return err
	}
	if !found {
		return ErrModelRecordMissing
	}
	return nil
}

// FindMap 查询动态字段；常规业务模型使用 Find 映射到结构体。
func (m *Model) FindMap() (map[string]any, error) { return m.newModelQuery().FindMap() }

// SelectMaps 查询动态字段列表；常规业务模型使用 Select 映射到结构体切片。
func (m *Model) SelectMaps() ([]map[string]any, error) { return m.newModelQuery().SelectMaps() }

// findRecordRow 为记录绑定保留获取器执行前的数据库身份和已加载字段。
func (mq *ModelQuery) findRecordRow() (map[string]any, map[string]any, error) {
	row, err := mq.prepareQuery().Find()
	if err != nil || row == nil {
		return nil, nil, err
	}
	raw := cloneDatabaseMap(row)
	if len(mq.relations) > 0 {
		if err := mq.loadRelations([]map[string]any{row}); err != nil {
			return nil, nil, err
		}
	}
	return mq.model.applyGetters(row), raw, nil
}

func (mq *ModelQuery) selectRecordRows() ([]map[string]any, []map[string]any, error) {
	rows, err := mq.prepareQuery().Select()
	if err != nil {
		return nil, nil, err
	}
	return mq.prepareRecordRows(rows)
}

// prepareRecordRows 在关联和获取器处理前保留原始字段，游标同样使用原始数据库值。
func (mq *ModelQuery) prepareRecordRows(rows []map[string]any) ([]map[string]any, []map[string]any, error) {
	raw := make([]map[string]any, len(rows))
	for i, row := range rows {
		raw[i] = cloneDatabaseMap(row)
	}
	if len(mq.relations) > 0 {
		if err := mq.loadRelations(rows); err != nil {
			return nil, nil, err
		}
	}
	for _, row := range rows {
		mq.model.applyGetters(row)
	}
	return rows, raw, nil
}
