package db

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
)

var (
	// ErrModelIdentityChanged 表示已加载记录的主键或表配置被修改。
	ErrModelIdentityChanged = errors.New("已加载模型的身份被修改")
	// ErrModelIdentityMissing 表示记录未加载主键，不能进行实例写操作。
	ErrModelIdentityMissing = errors.New("模型记录缺少已加载的主键")
	// ErrModelFieldNotLoaded 表示隐式保存修改了未加载字段，必须用 SaveFields 显式选择。
	ErrModelFieldNotLoaded = errors.New("修改了未加载的模型字段")
	// ErrModelRecordMissing 表示记录已被删除或当前更新未匹配记录。
	ErrModelRecordMissing = errors.New("模型记录不存在")
	// ErrModelRecordBusy 表示同一记录发生并发或回调重入写操作。
	ErrModelRecordBusy = errors.New("模型记录正在保存")
	// ErrModelRecordTransaction 表示记录依赖未提交或已回滚的事务，必须完成事务或重新查询。
	ErrModelRecordTransaction = fmt.Errorf("%w: 模型记录的事务结果尚未提交", ErrInvalidTransaction)
	// ErrModelRecordStale 表示实例已被重新查询替换，旧模型句柄不能继续保存。
	ErrModelRecordStale = errors.New("模型记录已被新的查询结果替换")
)

type modelRecord struct {
	mu         sync.Mutex
	owner      reflect.Value
	original   map[string]any
	loaded     map[string]bool
	identity   any
	persisted  bool
	deleted    bool
	uncertain  bool
	table      string
	primaryKey string
	database   *DB
	tx         *Tx
}

func (mq *ModelQuery) bindScannedRecord(candidate, destination reflect.Value, raw map[string]any) error {
	metadata, err := loadModelMetadata(candidate.Type())
	if err != nil {
		return err
	}
	if metadata.modelPath == nil {
		return nil
	}
	original, err := snapshotModelFields(candidate)
	if err != nil {
		return err
	}
	bound := mq.model.cloneBinding()
	bound.ctx = mq.query.context()
	bound.transaction = mq.query.txOwner
	record := &modelRecord{owner: destination, persisted: true, original: original, loaded: make(map[string]bool, len(raw)), table: bound.table, primaryKey: bound.primaryKeyField(), database: bound.db, tx: mq.query.txOwner}
	for key := range raw {
		record.loaded[key] = true
	}
	record.identity = cloneDatabaseValue(raw[record.primaryKey])
	if record.identity == nil && mq.query.insertPrimaryKey != record.primaryKey {
		record.identity = cloneDatabaseValue(raw[mq.query.insertPrimaryKey])
		record.loaded[record.primaryKey] = record.identity != nil
	}
	if bound.softDelete {
		record.deleted = raw[bound.deleteTimeField] != nil
	}
	bound.record = record
	field, err := writableModelScanField(candidate, metadata.modelPath)
	if err != nil {
		return err
	}
	field.Set(reflect.ValueOf(bound))
	return nil
}

// SaveFields 仅保存显式列出的业务字段，允许为未加载列赋新值。
// 未列出的脏字段仍保留，后续 Save 会继续保存这些修改。
func (m *Model) SaveFields(fields ...string) error {
	if len(fields) == 0 {
		return fmt.Errorf("%w: SaveFields 至少指定一个字段", ErrInvalidModel)
	}
	return m.saveRecord(fields)
}

func (m *Model) recordForWrite() (*modelRecord, error) {
	if err := m.operationReady(); err != nil {
		return nil, err
	}
	if m.record == nil {
		return nil, fmt.Errorf("%w: 记录未绑定，请使用 NewModelFor 或模型查询", ErrInvalidModel)
	}
	r := m.record
	if !r.mu.TryLock() {
		return nil, ErrModelRecordBusy
	}
	if err := r.validateOwner(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if r.uncertain {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: 请重新查询记录后操作", ErrPartialWrite)
	}
	if r.tx != nil && r.tx != m.transaction {
		r.tx.mu.Lock()
		committed := r.tx.state == transactionCommitted
		r.tx.mu.Unlock()
		if !committed {
			r.mu.Unlock()
			return nil, fmt.Errorf("%w: 请提交原事务或重新查询记录", ErrModelRecordTransaction)
		}
		r.tx = nil
	}
	return r, nil
}

func (r *modelRecord) validateOwner() error {
	metadata, err := loadModelMetadata(r.owner.Type())
	if err != nil {
		return err
	}
	base := modelFieldValue(r.owner, metadata.modelPath, false)
	if base.IsNil() || base.Interface().(*Model).record != r {
		return ErrModelRecordStale
	}
	return nil
}

func (m *Model) validateRecordIdentity(r *modelRecord, snapshot map[string]any) error {
	pk := m.primaryKeyField()
	if !r.persisted || !r.loaded[pk] || isZeroDBValue(r.identity) {
		return ErrModelIdentityMissing
	}
	if r.table != m.table || r.database != m.db || r.primaryKey != pk || !reflect.DeepEqual(snapshot[pk], r.original[pk]) {
		return ErrModelIdentityChanged
	}
	return nil
}

func (m *Model) saveRecord(fields []string) error {
	r, err := m.recordForWrite()
	if err != nil {
		return err
	}
	defer r.mu.Unlock()
	if r.deleted {
		return ErrModelRecordMissing
	}
	current, err := snapshotModelFields(r.owner)
	if err != nil {
		return err
	}
	metadata, err := loadModelMetadata(r.owner.Type())
	if err != nil {
		return err
	}
	selected := make(map[string]bool, len(fields))
	for _, column := range fields {
		if _, ok := current[column]; !ok || column == m.primaryKeyField() {
			return fmt.Errorf("%w: 不可保存字段 %q", ErrInvalidModel, column)
		}
		selected[column] = true
	}
	if !r.persisted {
		if len(fields) != 0 {
			return fmt.Errorf("%w: 新记录请使用 Save 或 Create 创建", ErrInvalidModel)
		}
		return m.insertOwnedRecord(r)
	}
	if err := m.validateRecordIdentity(r, current); err != nil {
		return err
	}
	data := make(map[string]any)
	for _, field := range metadata.fields {
		if field.readOnly {
			continue
		}
		column := field.column
		if column == r.primaryKey || (len(fields) > 0 && !selected[column]) {
			continue
		}
		changed := !reflect.DeepEqual(current[column], r.original[column])
		if !changed && !(selected[column] && !r.loaded[column]) {
			continue
		}
		if !r.loaded[column] && !selected[column] {
			return fmt.Errorf("%w: %s；请显式使用 SaveFields", ErrModelFieldNotLoaded, column)
		}
		data[column] = modelFieldValue(r.owner, field.indexPath, false).Interface()
	}
	if len(data) == 0 {
		return nil
	}
	query := m.newModelQuery().Where(r.primaryKey+" = ?", r.identity)
	result, err := query.updateResult(data)
	if err != nil {
		return err
	}
	r.tx = m.transaction
	if result.MatchedKnown && result.Matched == 0 {
		return ErrModelRecordMissing
	}
	if !result.MatchedKnown && result.Affected == 0 {
		count, err := query.Count()
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrModelRecordMissing
		}
	}
	if err := m.syncRecordTimestamps(r, result.Data, false); err != nil {
		r.uncertain = true
		return fmt.Errorf("%w: %w", ErrPartialWrite, err)
	}
	updated, err := snapshotModelFields(r.owner)
	if err != nil {
		r.uncertain = true
		return fmt.Errorf("%w: %w", ErrPartialWrite, err)
	}
	for column := range data {
		r.original[column] = current[column]
		r.loaded[column] = true
	}
	if m.autoTimestamp {
		if value, ok := updated[m.updateTimeField]; ok {
			r.original[m.updateTimeField] = value
			r.loaded[m.updateTimeField] = true
		}
	}
	return nil
}

func (m *Model) insertOwnedRecord(r *modelRecord) error {
	before, err := snapshotModelFields(r.owner)
	if err != nil {
		return err
	}
	binding, zero, err := m.preparePrimaryKey(r.owner)
	if err != nil {
		return err
	}
	data, err := m.structToMap(r.owner.Addr().Interface())
	if err != nil {
		return err
	}
	pk := m.primaryKeyField()
	if zero {
		delete(data, pk)
	}
	result, err := m.newModelQuery().insertResult(data, zero)
	if err != nil {
		r.uncertain = errors.Is(err, ErrPartialWrite)
		return err
	}
	r.tx = m.transaction
	if result.Affected != 1 {
		r.uncertain = true
		return fmt.Errorf("%w: 创建记录影响行数为 %d", ErrPartialWrite, result.Affected)
	}
	if zero {
		id, err := result.InsertedID()
		if err == nil {
			err = binding.Assign(id)
		}
		if err != nil {
			r.uncertain = true
			return &PartialWriteError{Result: result, Cause: err}
		}
	} else {
		// 主键修改器和插入事件可能规范化业务主键，后续写入必须使用最终身份。
		if err := binding.Assign(result.Data[pk]); err != nil {
			r.uncertain = true
			return &PartialWriteError{Result: result, Cause: err}
		}
	}
	if err := m.syncRecordTimestamps(r, result.Data, true); err != nil {
		r.uncertain = true
		return &PartialWriteError{Result: result, Cause: err}
	}
	snapshot, err := snapshotModelFields(r.owner)
	if err != nil {
		r.uncertain = true
		return &PartialWriteError{Result: result, Cause: err}
	}
	r.persisted = true
	// 回调在写入后对实例作出的修改仍是脏字段，仅数据库生成字段推进为最终值。
	before[pk] = snapshot[pk]
	if m.autoTimestamp {
		for _, column := range []string{m.createTimeField, m.updateTimeField} {
			if value, exists := snapshot[column]; exists {
				before[column] = value
			}
		}
	}
	r.original = before
	r.identity = cloneDatabaseValue(binding.field.Interface())
	r.loaded = make(map[string]bool, len(result.Data)+1)
	for column := range result.Data {
		r.loaded[column] = true
	}
	r.loaded[pk] = true
	r.table, r.primaryKey, r.database = m.table, pk, m.db
	return nil
}

func (m *Model) syncRecordTimestamps(r *modelRecord, data map[string]any, inserted bool) error {
	if !m.autoTimestamp {
		return nil
	}
	fields := []string{m.updateTimeField}
	if inserted {
		fields = append(fields, m.createTimeField)
	}
	for _, column := range fields {
		value, exists := data[column]
		if !exists {
			continue
		}
		field, _, found, err := findModelColumn(r.owner, column)
		if err != nil {
			return err
		}
		if found {
			if err := assignModelScanValue(field, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Model) deleteOwnedRecord(force, restore bool) error {
	r, err := m.recordForWrite()
	if err != nil {
		return err
	}
	defer r.mu.Unlock()
	current, err := snapshotModelFields(r.owner)
	if err != nil {
		return err
	}
	if err := m.validateRecordIdentity(r, current); err != nil {
		return err
	}
	query := m.newModelQuery().Where(r.primaryKey+" = ?", r.identity)
	var count int64
	var persisted map[string]any
	switch {
	case restore:
		var result UpdateResult
		result, err = query.restoreResult()
		count, persisted = result.Count(), result.Data
	case force:
		count, err = query.ForceDelete()
	default:
		count, persisted, err = query.deleteResult()
	}
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrModelRecordMissing
	}
	r.tx = m.transaction
	r.deleted = !restore
	if m.softDelete && !force {
		if err := m.syncRecordTimestamps(r, persisted, false); err != nil {
			r.uncertain = true
			return fmt.Errorf("%w: %w", ErrPartialWrite, err)
		}
		field, _, found, err := findModelColumn(r.owner, m.deleteTimeField)
		if err != nil {
			r.uncertain = true
			return fmt.Errorf("%w: %w", ErrPartialWrite, err)
		}
		if found {
			if err := assignModelScanValue(field, persisted[m.deleteTimeField]); err != nil {
				r.uncertain = true
				return fmt.Errorf("%w: %w", ErrPartialWrite, err)
			}
		}
		snapshot, err := snapshotModelFields(r.owner)
		if err != nil {
			r.uncertain = true
			return fmt.Errorf("%w: %w", ErrPartialWrite, err)
		}
		for _, column := range []string{m.deleteTimeField, m.updateTimeField} {
			if value, exists := snapshot[column]; exists {
				r.original[column] = value
				r.loaded[column] = true
			}
		}
	}
	return nil
}
