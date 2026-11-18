package db

import (
	"fmt"
	"reflect"
)

func (m *Model) ownsRecord(value any) bool {
	if m == nil || m.record == nil {
		return false
	}
	rv := reflect.ValueOf(value)
	return rv.IsValid() && rv.Kind() == reflect.Pointer && !rv.IsNil() && rv == m.record.owner.Addr()
}

func (m *Model) resolveOwnedRecordWriter(value any) (*Model, bool, error) {
	if m != nil && m.ownsRecord(value) {
		return m, true, nil
	}
	rv := reflect.ValueOf(value)
	if !rv.IsValid() || rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil, false, nil
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return nil, false, nil
	}
	metadata, err := loadModelMetadata(elem.Type())
	if err != nil || metadata.modelPath == nil {
		return nil, false, nil
	}
	base := modelFieldValue(elem, metadata.modelPath, false)
	if !base.IsValid() || base.IsNil() {
		return nil, false, nil
	}
	embedded, ok := base.Interface().(*Model)
	if !ok || !embedded.ownsRecord(value) {
		return nil, false, nil
	}
	if err := m.operationReady(); err != nil {
		return nil, false, err
	}
	if m.record != nil && m.record != embedded.record {
		return nil, false, fmt.Errorf("%w: 模型实例已绑定其他记录", ErrInvalidModel)
	}
	writer := m.cloneBinding()
	writer.record = embedded.record
	writer.modelType = elem.Type()

	embedded.mu.RLock()
	embeddedDB := embedded.db
	embeddedTable := embedded.table
	embeddedAutoTS := embedded.autoTimestamp
	embeddedCreateField := embedded.createTimeField
	embeddedUpdateField := embedded.updateTimeField
	embeddedTSType := embedded.timestampValueType
	embeddedSoftDelete := embedded.softDelete
	embeddedDeleteField := embedded.deleteTimeField
	embeddedCtx := embedded.ctx
	embedded.mu.RUnlock()
	embeddedPK := embedded.primaryKeyField()

	if !embedded.record.persisted {
		if writer.db != embeddedDB || writer.table != embeddedTable || writer.primaryKeyField() != embeddedPK {
			return nil, false, ErrModelIdentityChanged
		}
	}
	if !writer.softDelete && embeddedSoftDelete {
		writer.softDelete = true
		writer.deleteTimeField = embeddedDeleteField
	}
	if !writer.autoTimestamp && embeddedAutoTS {
		writer.autoTimestamp = true
		writer.createTimeField = embeddedCreateField
		writer.updateTimeField = embeddedUpdateField
		writer.timestampValueType = embeddedTSType
	}
	if writer.ctx == nil && embeddedCtx != nil {
		writer.ctx = embeddedCtx
	}
	return writer, true, nil
}

// Save 保存绑定记录；显式传入结构体时按主键执行创建或更新。
func (m *Model) Save(values ...any) error {
	if len(values) == 0 {
		return m.saveRecord(nil, false)
	}
	if len(values) != 1 {
		return fmt.Errorf("%w: Save 最多接收一个结构体", ErrInvalidModel)
	}
	v := values[0]
	if writer, owned, err := m.resolveOwnedRecordWriter(v); err != nil {
		return err
	} else if owned {
		return writer.saveRecord(nil, false)
	}
	if err := m.operationReady(); err != nil {
		return err
	}
	value, err := writableModelStruct(v)
	if err != nil {
		return err
	}
	binding, primaryIsZero, err := m.findPrimaryKeyBinding(value)
	if err != nil {
		return err
	}
	if primaryIsZero {
		return m.createWithBinding(v, value.Type(), binding)
	}
	return m.Update(v)
}

// Delete 按绑定记录主键删除，软删除模型自动更新时间戳。
func (m *Model) Delete(values ...any) error {
	if len(values) == 0 {
		if m != nil && m.record != nil {
			return m.deleteOwnedRecord(false, false)
		}
		_, err := m.newModelQuery().Delete()
		return err
	}
	if len(values) != 1 {
		return fmt.Errorf("%w: Delete 最多接收一个结构体", ErrInvalidModel)
	}
	v := values[0]
	if writer, owned, err := m.resolveOwnedRecordWriter(v); err != nil {
		return err
	} else if owned {
		return writer.deleteOwnedRecord(false, false)
	}
	if err := m.operationReady(); err != nil {
		return err
	}
	value, err := writableModelStruct(v)
	if err != nil {
		return err
	}
	data, err := m.structToMap(v)
	if err != nil {
		return err
	}
	pk := m.primaryKeyField()
	pkVal, ok := data[pk]
	if !ok || isZeroDBValue(pkVal) {
		return fmt.Errorf("%w: 结构体删除需要非零主键 %q 作为删除条件", ErrInvalidModel, pk)
	}
	_, err = m.newModelQuery().withModelType(value.Type()).Where(pk+" = ?", pkVal).Delete()
	return err
}

// ForceDelete 按绑定记录主键强制物理删除。
func (m *Model) ForceDelete() error {
	if m != nil && m.record != nil {
		return m.deleteOwnedRecord(true, false)
	}
	_, err := m.newModelQuery().ForceDelete()
	return err
}

// Restore 恢复绑定的软删除记录。
func (m *Model) Restore() error {
	if m != nil && m.record != nil {
		return m.deleteOwnedRecord(false, true)
	}
	_, err := m.newModelQuery().Restore()
	return err
}

// updateResult 保留匹配数与修改数，记录保存据此区分未修改和记录已被删除。
func (mq *ModelQuery) updateResult(data map[string]any) (UpdateResult, error) {
	if err := mq.validationError(); err != nil {
		return UpdateResult{}, err
	}
	// 框架自动追加的软删除条件不能替代业务 WHERE。
	if len(mq.query.where) == 0 {
		return UpdateResult{}, ErrUnsafeFullTableMutation
	}
	workingData, err := cloneDatabaseMap(data)
	if err != nil {
		return UpdateResult{}, err
	}
	mq.model.applySetters(workingData)
	workingData, allowed, err := mq.model.dispatchBeforeEvent(ModelBeforeUpdate, workingData)
	if err != nil {
		return UpdateResult{}, err
	}
	if !allowed {
		return UpdateResult{}, fmt.Errorf("before_update 事件回调阻止了更新操作")
	}
	q := mq.prepareQuery()
	if q.autoTimestamp {
		mq.seedTimestampFieldZeroValue(workingData, q.updateTimeField)
	}
	result, err := q.UpdateResult(workingData)
	if err != nil {
		return result, err
	}
	if err := mq.model.dispatchAfterEvent(ModelAfterUpdate, result.Data); err != nil {
		return result, fmt.Errorf("%w: %w", ErrPartialWrite, err)
	}
	return result, nil
}
