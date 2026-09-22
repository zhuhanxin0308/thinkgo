package db

import (
	"context"
	"fmt"
	"maps"
	"reflect"
)

// ModelOption 为一次独立模型绑定提供配置，不依赖全局数据库或隐式事务。
type ModelOption func(*Model) error

// WithModelTransaction 将新模型绑定到指定事务；不同数据库之间禁止借用事务。
func WithModelTransaction(tx *Tx) ModelOption {
	return func(model *Model) error {
		if tx == nil || model == nil || tx.db != model.db {
			return ErrInvalidTransaction
		}
		if err := tx.active(); err != nil {
			return err
		}
		model.transaction = tx
		return nil
	}
}

// NewModelFor 根据类型创建并注入模型。实例应通过指针使用，绑定后不能复制记录值。
// 模型可以实现 ConfigureModel(*Model) error，在同一文件中集中声明表名、主键和事件。
// 配置钩子通过参数配置模型；全部配置成功后才注入接收者的嵌入字段。
func NewModelFor(ctx context.Context, database *DB, instance any, options ...ModelOption) (*Model, error) {
	if ctx == nil {
		return nil, ErrInvalidDatabaseContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, err := writableModelStruct(instance)
	if err != nil {
		return nil, err
	}
	metadata, err := loadModelMetadata(value.Type())
	if err != nil {
		return nil, err
	}
	if metadata.modelPath == nil {
		return nil, fmt.Errorf("%w: 模型必须匿名嵌入 *db.Model", ErrInvalidModel)
	}
	if !modelFieldValue(value, metadata.modelPath, false).IsNil() {
		return nil, fmt.Errorf("%w: 实例已经绑定模型，请创建新实例", ErrInvalidModel)
	}
	model, err := NewModelAuto(database, instance)
	if err != nil {
		return nil, err
	}
	model.ctx = ctx
	model.record = &modelRecord{owner: value}
	// 提前分配匿名嵌入链，提升的配置方法才能安全访问其接收者；失败时恢复原值。
	original := reflect.New(value.Type()).Elem()
	original.Set(value)
	field, err := writableModelScanField(value, metadata.modelPath)
	if err != nil {
		return nil, err
	}
	configured := false
	defer func() {
		if !configured {
			value.Set(original)
		}
	}()
	if configure, ok := instance.(interface{ ConfigureModel(*Model) error }); ok {
		if err := configure.ConfigureModel(model); err != nil {
			return nil, err
		}
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("%w: 模型选项不能为空", ErrInvalidModel)
		}
		if err := option(model); err != nil {
			return nil, err
		}
	}
	if err := model.validationError(); err != nil {
		return nil, err
	}
	field.Set(reflect.ValueOf(model))
	configured = true
	return model, nil
}

// cloneBinding 复制配置容器，使派生模型之间的配置、上下文和事务互不污染。
func (m *Model) cloneBinding() *Model {
	if m == nil {
		return NewModel(nil, "")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	cloned := &Model{
		configErr: m.configErr, table: m.table, db: m.db,
		autoTimestamp: m.autoTimestamp, createTimeField: m.createTimeField,
		updateTimeField: m.updateTimeField, timestampValueType: m.timestampValueType,
		softDelete: m.softDelete, deleteTimeField: m.deleteTimeField,
		primaryKey: m.primaryKey, primaryKeyExplicit: m.primaryKeyExplicit,
		relations: maps.Clone(m.relations), getters: maps.Clone(m.getters),
		setters: maps.Clone(m.setters), searchers: maps.Clone(m.searchers),
		getterSnapshot: append([]modelGetterEntry(nil), m.getterSnapshot...),
		setterSnapshot: append([]modelSetterEntry(nil), m.setterSnapshot...),
		events:         make(map[ModelEventType][]ModelEventCallback, len(m.events)),
		ctx:            m.ctx, transaction: m.transaction, record: m.record,
	}
	for event, callbacks := range m.events {
		cloned.events[event] = append([]ModelEventCallback(nil), callbacks...)
	}
	return cloned
}

// WithContext 派生独立的模型执行上下文，同一记录的保存状态仍由记录自身持有。
func (m *Model) WithContext(ctx context.Context) *Model {
	cloned := m.cloneBinding()
	if ctx == nil {
		cloned.setConfigError(ErrInvalidDatabaseContext)
	} else {
		cloned.ctx = ctx
	}
	return cloned
}

// WithTx 派生事务模型，后续所有 CRUD 和关联查询均使用同一个事务。
func (m *Model) WithTx(tx *Tx) *Model {
	cloned := m.cloneBinding()
	if err := WithModelTransaction(tx)(cloned); err != nil {
		cloned.setConfigError(err)
	}
	return cloned
}

func (m *Model) runtimeQuery(database *DB, table string) *Query {
	m.mu.RLock()
	tx, ctx := m.transaction, m.ctx
	m.mu.RUnlock()
	query := newQueryWithOwner(database, table, false, tx)
	if ctx != nil {
		query.ctx = ctx
	}
	return query
}

func (mq *ModelQuery) newRelationTableQuery(database *DB, table string) *Query {
	if mq == nil || mq.query == nil {
		return newQuery(database, table)
	}
	return mq.inheritRelationRuntime(newQueryWithOwner(database, table, false, mq.query.txOwner))
}

// relationModelQuery 创建关联查询，同时继承请求上下文和事务来源。
func (mq *ModelQuery) relationModelQuery(model *Model) *ModelQuery {
	if model != nil && mq != nil && mq.query != nil && mq.query.txOwner != nil {
		model = model.cloneBinding()
		if model.db != mq.query.db || (model.transaction != nil && model.transaction != mq.query.txOwner) {
			model.setConfigError(fmt.Errorf("%w: 关联必须属于同一数据库和事务", ErrInvalidRelation))
		} else {
			model.transaction = mq.query.txOwner
		}
	}
	related := model.newModelQuery()
	related.query = mq.inheritRelationRuntime(related.query)
	return related
}

// inheritRelationRuntime 同时传递取消信号与事务，禁止关联偷偷切换执行器。
func (mq *ModelQuery) inheritRelationRuntime(query *Query) *Query {
	if query == nil || mq == nil || mq.query == nil {
		return query
	}
	parent := mq.query
	if parent.err != nil {
		return query.setError(parent.err)
	}
	if parent.txOwner != nil || query.txOwner != nil {
		if query.db != parent.db || (query.txOwner != nil && query.txOwner != parent.txOwner) {
			return query.setError(fmt.Errorf("%w: 关联必须属于同一数据库和事务", ErrInvalidRelation))
		}
		if parent.txOwner != nil {
			query = parent.txOwner.bindQuery(query)
		}
	}
	if parent.ctx != nil {
		query.ctx = parent.ctx
	}
	return query
}

func (m *Model) operationReady() error {
	if err := m.validationError(); err != nil {
		return err
	}
	m.mu.RLock()
	database, tx, ctx := m.db, m.transaction, m.ctx
	m.mu.RUnlock()
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if tx != nil {
		return tx.active()
	}
	return database.WithConnection(func(Connection) error { return nil })
}
