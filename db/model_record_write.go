package db

import (
	"fmt"
	"reflect"
)

func (m *Model) ownsRecord(value any) bool {
	return m != nil && m.record != nil && reflect.ValueOf(value).Kind() == reflect.Pointer && reflect.ValueOf(value) == m.record.owner.Addr()
}

// Save 保存绑定记录；显式传入结构体时按主键执行创建或更新。
func (m *Model) Save(values ...any) error {
	if len(values) == 0 {
		return m.saveRecord(nil)
	}
	if len(values) != 1 {
		return fmt.Errorf("%w: Save 最多接收一个结构体", ErrInvalidModel)
	}
	v := values[0]
	if m.ownsRecord(v) {
		return m.saveRecord(nil)
	}
	if err := m.operationReady(); err != nil {
		return err
	}
	value, err := writableModelStruct(v)
	if err != nil {
		return err
	}
	binding, primaryIsZero, err := m.preparePrimaryKey(value)
	if err != nil {
		return err
	}
	if primaryIsZero {
		return m.createWithBinding(v, binding, true)
	}
	return m.Update(v)
}

// Delete 按绑定记录主键删除，软删除模型自动更新时间戳。
func (m *Model) Delete() error {
	if m != nil && m.record != nil {
		return m.deleteOwnedRecord(false, false)
	}
	_, err := m.newModelQuery().Delete()
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
	workingData := cloneDatabaseMap(data)
	mq.model.applySetters(workingData)
	workingData, allowed := mq.model.dispatchBeforeEvent(ModelBeforeUpdate, workingData)
	if !allowed {
		return UpdateResult{}, fmt.Errorf("before_update 事件回调阻止了更新操作")
	}
	result, err := mq.prepareQuery().UpdateResult(workingData)
	if err != nil {
		return result, err
	}
	mq.model.dispatchAfterEvent(ModelAfterUpdate, result.Data)
	return result, nil
}
