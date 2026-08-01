package db

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	relationHasOne        = "hasOne"
	relationHasMany       = "hasMany"
	relationBelongsTo     = "belongsTo"
	relationBelongsToMany = "belongsToMany"
)

// RelationDefinition 描述模型之间的关联关系。
type RelationDefinition struct {
	Name              string
	Type              string
	Related           *Model
	ForeignKey        string
	LocalKey          string
	OwnerKey          string
	PivotTable        string // 多对多中间表名（对应 ThinkPHP 的 pivot 表）
	RelatedForeignKey string // 关联模型在中间表中的外键
}

// ModelEventType 模型事件类型。
// 对应 ThinkPHP 的 before_insert/after_insert 等模型事件。
type ModelEventType string

const (
	// ModelBeforeInsert 插入前事件
	ModelBeforeInsert ModelEventType = "before_insert"
	// ModelAfterInsert 插入后事件
	ModelAfterInsert ModelEventType = "after_insert"
	// ModelBeforeUpdate 更新前事件
	ModelBeforeUpdate ModelEventType = "before_update"
	// ModelAfterUpdate 更新后事件
	ModelAfterUpdate ModelEventType = "after_update"
	// ModelBeforeDelete 删除前事件
	ModelBeforeDelete ModelEventType = "before_delete"
	// ModelAfterDelete 删除后事件
	ModelAfterDelete ModelEventType = "after_delete"
)

// ModelEventCallback 模型事件回调函数。
// 返回 false 可阻止操作继续执行（仅 before_* 事件有效）。
type ModelEventCallback func(data map[string]interface{}) bool

// ModelGetterFunc 模型获取器函数类型。
// 接收原始字段值和整行数据，返回转换后的值。
// 对应 ThinkPHP 的 getXxxAttr($value, $data) 方法。
type ModelGetterFunc func(value interface{}, data map[string]interface{}) interface{}

// ModelSetterFunc 模型修改器函数类型。
// 接收原始字段值和整行数据，返回转换后的值。
// 对应 ThinkPHP 的 setXxxAttr($value, $data) 方法。
type ModelSetterFunc func(value interface{}, data map[string]interface{}) interface{}

// ModelSearcherFunc 模型搜索器函数类型。
// 接收 Query 对象和搜索值，在 Query 上追加条件。
// 对应 ThinkPHP 的 searchXxxAttr($query, $value, $data) 方法。
type ModelSearcherFunc func(query *Query, value interface{}, data map[string]interface{}) *Query

type modelGetterEntry struct {
	field  string
	getter ModelGetterFunc
}

type modelSetterEntry struct {
	field  string
	setter ModelSetterFunc
}

// Model 表示一个数据库模型，支持时间戳、软删除、关系定义、模型事件、获取器/修改器/搜索器。
type Model struct {
	mu                 sync.RWMutex
	configErr          error
	table              string
	db                 *DB
	autoTimestamp      bool
	createTimeField    string
	updateTimeField    string
	timestampValueType string
	softDelete         bool
	deleteTimeField    string
	primaryKey         string
	primaryKeyExplicit bool
	relations          map[string]RelationDefinition
	events             map[ModelEventType][]ModelEventCallback // 模型事件回调
	getters            map[string]ModelGetterFunc              // 获取器映射
	getterSnapshot     []modelGetterEntry                      // 按字段排序的不可变获取器快照
	setters            map[string]ModelSetterFunc              // 修改器映射
	setterSnapshot     []modelSetterEntry                      // 按字段排序的不可变修改器快照
	searchers          map[string]ModelSearcherFunc            // 搜索器映射
}

// NewModel 创建模型实例。
func NewModel(db *DB, table string) *Model {
	model := &Model{
		db:                 db,
		table:              table,
		createTimeField:    "create_time",
		updateTimeField:    "update_time",
		timestampValueType: TimestampValueTypeUnix,
		primaryKey:         "id",
		relations:          make(map[string]RelationDefinition),
		events:             make(map[ModelEventType][]ModelEventCallback),
		getters:            make(map[string]ModelGetterFunc),
		setters:            make(map[string]ModelSetterFunc),
		searchers:          make(map[string]ModelSearcherFunc),
	}
	if db == nil {
		model.configErr = fmt.Errorf("%w: 数据库不能为空", ErrInvalidModel)
	} else {
		db.mu.RLock()
		model.autoTimestamp = db.autoTimestamp
		model.createTimeField = db.createTimeField
		model.updateTimeField = db.updateTimeField
		model.timestampValueType = db.timestampValueType
		db.mu.RUnlock()
	}
	if err := validateIdentifier(table); err != nil {
		model.configErr = fmt.Errorf("%w: 非法模型表名: %w", ErrInvalidModel, err)
	}
	return model
}

func (m *Model) setConfigError(err error) {
	if m == nil || err == nil {
		return
	}
	m.mu.Lock()
	if m.configErr == nil {
		m.configErr = err
	}
	m.mu.Unlock()
}

func (m *Model) validationError() error {
	if m == nil {
		return fmt.Errorf("%w: 模型不能为空", ErrInvalidModel)
	}
	m.mu.RLock()
	err := m.configErr
	database := m.db
	table := m.table
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	if database == nil {
		return fmt.Errorf("%w: 数据库不能为空", ErrInvalidModel)
	}
	if err := validateIdentifier(table); err != nil {
		return fmt.Errorf("%w: 非法模型表名: %w", ErrInvalidModel, err)
	}
	return nil
}

// Getter 注册字段获取器。
// 查询结果返回时，自动对指定字段应用获取器转换。
// 对应 ThinkPHP 的 getFieldNameAttr 方法。
//
//	示例：model.Getter("status", func(v interface{}, data map[string]interface{}) interface{} {
//	    if v == 1 { return "启用" }
//	    return "禁用"
//	})
func (m *Model) Getter(field string, fn ModelGetterFunc) error {
	if m == nil || fn == nil {
		return fmt.Errorf("%w: 获取器不能为空", ErrInvalidModel)
	}
	if err := validateIdentifier(field); err != nil {
		return fmt.Errorf("%w: 非法获取器字段: %w", ErrInvalidModel, err)
	}
	m.mu.Lock()
	if _, duplicated := m.getters[field]; duplicated {
		m.mu.Unlock()
		return fmt.Errorf("%w: 字段 %q 的获取器已注册", ErrInvalidModel, field)
	}
	m.getters[field] = fn
	fields := sortedModelCallbackFields(m.getters)
	snapshot := make([]modelGetterEntry, 0, len(fields))
	for _, registeredField := range fields {
		snapshot = append(snapshot, modelGetterEntry{field: registeredField, getter: m.getters[registeredField]})
	}
	m.getterSnapshot = snapshot
	m.mu.Unlock()
	return nil
}

// Setter 注册字段修改器。
// 插入或更新数据时，自动对指定字段应用修改器转换。
// 对应 ThinkPHP 的 setFieldNameAttr 方法。
//
//	示例：model.Setter("password", func(v interface{}, data map[string]interface{}) interface{} {
//	    return md5(v.(string))
//	})
func (m *Model) Setter(field string, fn ModelSetterFunc) error {
	if m == nil || fn == nil {
		return fmt.Errorf("%w: 修改器不能为空", ErrInvalidModel)
	}
	if err := validateIdentifier(field); err != nil {
		return fmt.Errorf("%w: 非法修改器字段: %w", ErrInvalidModel, err)
	}
	m.mu.Lock()
	if _, duplicated := m.setters[field]; duplicated {
		m.mu.Unlock()
		return fmt.Errorf("%w: 字段 %q 的修改器已注册", ErrInvalidModel, field)
	}
	m.setters[field] = fn
	fields := sortedModelSetterFields(m.setters)
	snapshot := make([]modelSetterEntry, 0, len(fields))
	for _, registeredField := range fields {
		snapshot = append(snapshot, modelSetterEntry{field: registeredField, setter: m.setters[registeredField]})
	}
	m.setterSnapshot = snapshot
	m.mu.Unlock()
	return nil
}

// Searcher 注册字段搜索器。
// 使用 WithSearch 时，自动将搜索条件映射为查询条件。
// 对应 ThinkPHP 的 searchFieldNameAttr 方法。
//
//	示例：model.Searcher("name", func(q *Query, v interface{}, data map[string]interface{}) *Query {
//	    return q.Where("name LIKE ?", "%"+v.(string)+"%")
//	})
func (m *Model) Searcher(field string, fn ModelSearcherFunc) error {
	if m == nil || fn == nil {
		return fmt.Errorf("%w: 搜索器不能为空", ErrInvalidModel)
	}
	if err := validateIdentifier(field); err != nil {
		return fmt.Errorf("%w: 非法搜索器字段: %w", ErrInvalidModel, err)
	}
	m.mu.Lock()
	if _, duplicated := m.searchers[field]; duplicated {
		m.mu.Unlock()
		return fmt.Errorf("%w: 字段 %q 的搜索器已注册", ErrInvalidModel, field)
	}
	m.searchers[field] = fn
	m.mu.Unlock()
	return nil
}

// applyGetters 对查询结果行应用获取器转换。
func (m *Model) applyGetters(row map[string]interface{}) map[string]interface{} {
	if m == nil || row == nil {
		return row
	}
	m.mu.RLock()
	getters := m.getterSnapshot
	m.mu.RUnlock()
	for _, entry := range getters {
		if val, ok := row[entry.field]; ok {
			row[entry.field] = entry.getter(val, row)
		}
	}
	return row
}

// applySetters 对写入数据应用修改器转换。
func (m *Model) applySetters(data map[string]interface{}) map[string]interface{} {
	if m == nil || data == nil {
		return data
	}
	m.mu.RLock()
	setters := m.setterSnapshot
	m.mu.RUnlock()
	for _, entry := range setters {
		if val, ok := data[entry.field]; ok {
			data[entry.field] = entry.setter(val, data)
		}
	}
	return data
}

// WithSearch 使用搜索器批量应用查询条件。
// 对应 ThinkPHP 的 withSearch(['name', 'status'], $data) 方法。
// fields: 需要搜索的字段名列表
// data: 搜索值映射（field → value）
func (m *Model) WithSearch(fields []string, data map[string]interface{}) *ModelQuery {
	mq := m.newModelQuery()
	if m == nil {
		return mq
	}
	m.mu.RLock()
	searchers := make(map[string]ModelSearcherFunc, len(m.searchers))
	for field, searcher := range m.searchers {
		searchers[field] = searcher
	}
	m.mu.RUnlock()
	searchData := cloneDatabaseMap(data)
	for _, field := range fields {
		searcher, ok := searchers[field]
		if !ok {
			continue
		}
		value, exists := searchData[field]
		if !exists {
			continue
		}
		derived := searcher(mq.query, value, cloneDatabaseMap(searchData))
		if derived == nil {
			mq.query = mq.query.setError(fmt.Errorf("%w: 搜索器 %q 返回空 Query", ErrInvalidModel, field))
			continue
		}
		mq.query = derived
	}
	return mq
}

// On 注册模型事件回调。
// 对应 ThinkPHP 的 Model::event() 静态方法。
func (m *Model) On(eventType ModelEventType, callback ModelEventCallback) error {
	if m == nil || callback == nil || !validModelEventType(eventType) {
		return fmt.Errorf("%w: 模型事件类型或回调非法", ErrInvalidModel)
	}
	m.mu.Lock()
	m.events[eventType] = append(m.events[eventType], callback)
	m.mu.Unlock()
	return nil
}

func sortedModelCallbackFields(callbacks map[string]ModelGetterFunc) []string {
	fields := make([]string, 0, len(callbacks))
	for field := range callbacks {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

func sortedModelSetterFields(setters map[string]ModelSetterFunc) []string {
	fields := make([]string, 0, len(setters))
	for field := range setters {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

func validModelEventType(eventType ModelEventType) bool {
	switch eventType {
	case ModelBeforeInsert, ModelAfterInsert, ModelBeforeUpdate, ModelAfterUpdate, ModelBeforeDelete, ModelAfterDelete:
		return true
	default:
		return false
	}
}

// NewModelAuto 根据结构体名称自动推断表名。
// 默认规则仅做驼峰转下划线和小写化，不再自动复数化。
func NewModelAuto(db *DB, model interface{}) (*Model, error) {
	table, err := GetTableName(model)
	if err != nil {
		return nil, err
	}
	created := NewModel(db, table)
	if err := created.validationError(); err != nil {
		return nil, err
	}
	return created, nil
}

// GetTableName 从结构体类型推断表名。
// 表前缀由 DB 配置统一追加，这里只负责生成不带前缀的小写表名。
func GetTableName(model interface{}) (string, error) {
	t := reflect.TypeOf(model)
	if t == nil {
		return "", fmt.Errorf("%w: 无法从 nil 推断表名", ErrInvalidModel)
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t.Name() == "" {
		return "", fmt.Errorf("%w: 表名推断要求具名结构体，实际为 %s", ErrInvalidModel, t.Kind())
	}
	return ToSnakeCase(t.Name()), nil
}

// SetDB 设置数据库连接。
func (m *Model) SetDB(db *DB) error {
	if m == nil || db == nil {
		return fmt.Errorf("%w: 数据库不能为空", ErrInvalidModel)
	}
	m.mu.Lock()
	m.db = db
	m.mu.Unlock()
	return nil
}

// Table 设置表名。
func (m *Model) Table(name string) *Model {
	if m == nil {
		return m
	}
	if err := validateIdentifier(name); err != nil {
		m.setConfigError(fmt.Errorf("%w: 非法模型表名: %w", ErrInvalidModel, err))
		return m
	}
	m.mu.Lock()
	m.table = name
	m.mu.Unlock()
	return m
}

// AutoTimestamp 设置是否自动维护时间戳。
func (m *Model) AutoTimestamp(enable bool) *Model {
	if m == nil {
		return m
	}
	m.mu.Lock()
	m.autoTimestamp = enable
	m.mu.Unlock()
	return m
}

// CreateTimeField 设置创建时间字段。
func (m *Model) CreateTimeField(name string) *Model {
	if m == nil {
		return m
	}
	if err := validateIdentifier(name); err != nil {
		m.setConfigError(fmt.Errorf("%w: 非法创建时间字段: %w", ErrInvalidModel, err))
		return m
	}
	m.mu.Lock()
	m.createTimeField = name
	m.mu.Unlock()
	return m
}

// UpdateTimeField 设置更新时间字段。
func (m *Model) UpdateTimeField(name string) *Model {
	if m == nil {
		return m
	}
	if err := validateIdentifier(name); err != nil {
		m.setConfigError(fmt.Errorf("%w: 非法更新时间字段: %w", ErrInvalidModel, err))
		return m
	}
	m.mu.Lock()
	m.updateTimeField = name
	m.mu.Unlock()
	return m
}

// PrimaryKey 设置主键字段名（默认 "id"）。
func (m *Model) PrimaryKey(name string) *Model {
	if m == nil {
		return m
	}
	if err := validateIdentifier(name); err != nil {
		m.setConfigError(fmt.Errorf("%w: 非法主键字段: %w", ErrInvalidModel, err))
		return m
	}
	m.mu.Lock()
	m.primaryKey = name
	m.primaryKeyExplicit = true
	m.mu.Unlock()
	return m
}

// primaryKeyField 返回主键字段名，未配置时回退到 "id"。
func (m *Model) primaryKeyField() string {
	if m == nil {
		return "id"
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.primaryKey == "" {
		return "id"
	}
	return m.primaryKey
}

func resolveModelStoragePrimaryKey(database *DB, modelKey string, explicitlyConfigured bool) (string, error) {
	if database == nil {
		return modelKey, ErrDatabaseUnavailable
	}
	database.mu.RLock()
	managed := database.connection
	database.mu.RUnlock()
	if managed == nil || isNilDatabaseDependency(managed.connection) {
		return modelKey, ErrDatabaseUnavailable
	}
	storageKey := modelKey
	if mapper, ok := managed.connection.(ModelPrimaryKeyMapper); ok {
		storageKey = mapper.StoragePrimaryKey(modelKey, explicitlyConfigured)
	}
	if err := validateIdentifier(storageKey); err != nil {
		return modelKey, fmt.Errorf("%w: invalid storage primary key: %v", ErrInvalidModel, err)
	}
	return storageKey, nil
}

// SoftDelete 开启软删除。
func (m *Model) SoftDelete(deleteField ...string) *Model {
	if m == nil {
		return m
	}
	field := "delete_time"
	if len(deleteField) > 1 {
		m.setConfigError(fmt.Errorf("%w: SoftDelete 最多接收一个字段", ErrInvalidModel))
		return m
	}
	if len(deleteField) == 1 && deleteField[0] != "" {
		field = deleteField[0]
	}
	if err := validateIdentifier(field); err != nil {
		m.setConfigError(fmt.Errorf("%w: 非法软删除字段: %w", ErrInvalidModel, err))
		return m
	}
	m.mu.Lock()
	m.softDelete = true
	m.deleteTimeField = field
	m.mu.Unlock()
	return m
}

// WithTrashed 查询包含已软删除数据。
func (m *Model) WithTrashed() *ModelQuery {
	mq := m.newModelQuery()
	mq.withTrashed = true
	return mq
}

// OnlyTrashed 仅查询已软删除数据。
func (m *Model) OnlyTrashed() *ModelQuery {
	mq := m.newModelQuery()
	if m == nil {
		return mq
	}
	m.mu.RLock()
	softDelete := m.softDelete
	m.mu.RUnlock()
	if !softDelete {
		mq.query.setError(fmt.Errorf("%w: OnlyTrashed 仅适用于软删除模型", ErrInvalidModel))
		return mq
	}
	mq.onlyTrashed = true
	return mq
}

// With 预加载指定关联。
func (m *Model) With(relations ...string) *ModelQuery {
	mq := m.newModelQuery()
	mq.relations = append([]string(nil), relations...)
	return mq
}

// DefineHasOne 定义一对一关联。
func (m *Model) DefineHasOne(name string, relatedModel *Model, foreignKey string, localKey string) error {
	return m.registerRelation(RelationDefinition{
		Name:       name,
		Type:       relationHasOne,
		Related:    relatedModel,
		ForeignKey: foreignKey,
		LocalKey:   localKey,
	})
}

// DefineHasMany 定义一对多关联。
func (m *Model) DefineHasMany(name string, relatedModel *Model, foreignKey string, localKey string) error {
	return m.registerRelation(RelationDefinition{
		Name:       name,
		Type:       relationHasMany,
		Related:    relatedModel,
		ForeignKey: foreignKey,
		LocalKey:   localKey,
	})
}

// DefineBelongsTo 定义反向关联。
func (m *Model) DefineBelongsTo(name string, relatedModel *Model, foreignKey string, ownerKey string) error {
	return m.registerRelation(RelationDefinition{
		Name:       name,
		Type:       relationBelongsTo,
		Related:    relatedModel,
		ForeignKey: foreignKey,
		OwnerKey:   ownerKey,
	})
}

// DefineBelongsToMany 定义多对多关联。
// pivotTable: 中间表名（不含前缀，框架会自动拼接）
// foreignKey: 当前模型在中间表中的外键字段名
// relatedForeignKey: 关联模型在中间表中的外键字段名
// localKey: 当前模型的主键字段名
// 对应 ThinkPHP 的 $this->belongsToMany(Role::class, 'user_role', 'role_id', 'user_id')
func (m *Model) DefineBelongsToMany(name string, relatedModel *Model, pivotTable string, foreignKey string, relatedForeignKey string, localKey string) error {
	return m.registerRelation(RelationDefinition{
		Name:              name,
		Type:              relationBelongsToMany,
		Related:           relatedModel,
		ForeignKey:        foreignKey,
		LocalKey:          localKey,
		PivotTable:        pivotTable,
		RelatedForeignKey: relatedForeignKey,
	})
}

func (m *Model) registerRelation(definition RelationDefinition) error {
	if m == nil || definition.Related == nil {
		return fmt.Errorf("%w: 当前模型和关联模型不能为空", ErrInvalidRelation)
	}
	if err := m.validationError(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRelation, err)
	}
	if err := definition.Related.validationError(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRelation, err)
	}
	identifiers := []string{definition.Name, definition.ForeignKey}
	switch definition.Type {
	case relationHasOne, relationHasMany:
		identifiers = append(identifiers, definition.LocalKey)
	case relationBelongsTo:
		identifiers = append(identifiers, definition.OwnerKey)
	case relationBelongsToMany:
		identifiers = append(identifiers, definition.LocalKey, definition.PivotTable, definition.RelatedForeignKey)
	default:
		return fmt.Errorf("%w: 未知关联类型 %q", ErrInvalidRelation, definition.Type)
	}
	for _, identifier := range identifiers {
		if err := validateIdentifier(identifier); err != nil {
			return fmt.Errorf("%w: 非法关联标识符 %q: %w", ErrInvalidRelation, identifier, err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, duplicated := m.relations[definition.Name]; duplicated {
		return fmt.Errorf("%w: 关联 %q 已定义", ErrInvalidRelation, definition.Name)
	}
	m.relations[definition.Name] = definition
	return nil
}

// BelongsToMany 立即查询多对多关联。
// 通过中间表进行两次查询：先查中间表取关联 ID，再查关联表取数据。
func (m *Model) BelongsToMany(relatedModel *Model, pivotTable string, foreignKey string, relatedForeignKey string, localKeyValue interface{}) ([]map[string]interface{}, error) {
	if err := validateImmediateRelation(m, relatedModel, pivotTable, foreignKey, relatedForeignKey); err != nil {
		return nil, err
	}
	m.mu.RLock()
	database := m.db
	m.mu.RUnlock()
	// 1. 查询中间表，获取关联模型的 ID 列表
	pivotRows, err := database.Name(pivotTable).Field(relatedForeignKey).Where(foreignKey+" = ?", localKeyValue).Select()
	if err != nil {
		return nil, fmt.Errorf("查询中间表 %s 失败: %w", pivotTable, err)
	}
	if len(pivotRows) == 0 {
		return []map[string]interface{}{}, nil
	}

	// 2. 提取关联模型 ID
	relatedKeys, err := collectRelationKeys(pivotRows, relatedForeignKey)
	if err != nil {
		return nil, err
	}
	if len(relatedKeys) == 0 {
		return []map[string]interface{}{}, nil
	}

	// 3. 查询关联模型
	return relatedModel.newModelQuery().WhereIn(relatedModel.primaryKeyField(), relatedKeys).Select()
}

// query 返回当前模型对应的查询对象。
// Model 存储的是短表名，使用 Name() 自动拼接前缀。
func (m *Model) query() *Query {
	return m.newModelQuery().prepareQuery()
}

// Where 添加查询条件。
func (m *Model) Where(condition interface{}, args ...interface{}) *ModelQuery {
	return m.newModelQuery().Where(condition, args...)
}

// Find 查询单条记录，自动应用获取器。
func (m *Model) Find() (map[string]interface{}, error) {
	return m.newModelQuery().Find()
}

// Select 查询多条记录，自动应用获取器。
func (m *Model) Select() ([]map[string]interface{}, error) {
	return m.newModelQuery().Select()
}

// Each 按行消费模型查询结果，适合大结果集且不会一次性保留全部行。
func (m *Model) Each(callback func(row map[string]interface{}) bool) error {
	return m.newModelQuery().Each(callback)
}

// Count 统计记录总数。
func (m *Model) Count() (int64, error) {
	return m.newModelQuery().Count()
}

// Insert 使用 map 插入记录并返回影响行数。
// 自动应用修改器，触发 before_insert/after_insert 模型事件。
func (m *Model) Insert(data map[string]interface{}) (int64, error) {
	return m.newModelQuery().Insert(data)
}

// InsertGetId 使用 map 插入记录并返回驱动报告的真实主键。
func (m *Model) InsertGetId(data map[string]interface{}) (interface{}, error) {
	return m.newModelQuery().InsertGetId(data)
}

// UpdateMap 使用 map 更新记录。
// 自动应用修改器，触发 before_update/after_update 模型事件。
func (m *Model) UpdateMap(data map[string]interface{}) (int64, error) {
	return m.newModelQuery().Update(data)
}

// DeleteRecord 删除记录。
// 触发 before_delete/after_delete 模型事件。
func (m *Model) DeleteRecord() (int64, error) {
	return m.newModelQuery().Delete()
}

// Order 设置排序。
func (m *Model) Order(order string) *ModelQuery {
	return m.newModelQuery().Order(order)
}

// Limit 设置查询数量限制。
func (m *Model) Limit(limit int) *ModelQuery {
	return m.newModelQuery().Limit(limit)
}

// Field 设置查询字段。
func (m *Model) Field(fields string) *ModelQuery {
	return m.newModelQuery().Field(fields)
}

// Page 设置分页参数。
func (m *Model) Page(page int, pageSize int) *ModelQuery {
	return m.newModelQuery().Page(page, pageSize)
}

// Create 从结构体创建记录。
// 自动应用修改器并触发 before_insert/after_insert 事件。
// 主键为零值时会被剔除，交由数据库自增生成，避免误插入 id=0。
func (m *Model) Create(v interface{}) error {
	if err := m.validationError(); err != nil {
		return err
	}
	value, err := writableModelStruct(v)
	if err != nil {
		return err
	}
	binding, primaryWasZero, err := m.preparePrimaryKey(value)
	if err != nil {
		return err
	}
	return m.createWithBinding(v, binding, primaryWasZero)
}

func (m *Model) createWithBinding(v interface{}, binding primaryKeyBinding, primaryWasZero bool) error {
	data, err := m.structToMap(v)
	if err != nil {
		return err
	}

	pk := m.primaryKeyField()
	if primaryWasZero {
		delete(data, pk)
	}

	result, err := m.newModelQuery().insertResult(data, true)
	if err != nil {
		return err
	}
	if primaryWasZero {
		id, idErr := result.InsertedID()
		if idErr != nil {
			return idErr
		}
		if assignErr := binding.Assign(id); assignErr != nil {
			return &PartialWriteError{Result: result, Cause: assignErr}
		}
	}
	return nil
}

// Save follows ThinkPHP model semantics: a zero primary key creates a record,
// while a non-zero primary key updates the matching record.
func (m *Model) Save(v interface{}) error {
	if err := m.validationError(); err != nil {
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

// Update 从结构体更新记录。
// 以主键作为 WHERE 条件按行更新，并将主键从 SET 子句中剔除，
// 避免无 WHERE 的全表更新和对主键的误写。
// 自动应用修改器并触发 before_update/after_update 事件。
func (m *Model) Update(v interface{}) error {
	if err := m.validationError(); err != nil {
		return err
	}
	if _, err := writableModelStruct(v); err != nil {
		return err
	}
	data, err := m.structToMap(v)
	if err != nil {
		return err
	}

	pk := m.primaryKeyField()
	pkVal, ok := data[pk]
	if !ok || isZeroDBValue(pkVal) {
		return fmt.Errorf("结构体更新需要非零主键 %q 作为更新条件", pk)
	}
	delete(data, pk)

	_, err = m.newModelQuery().Where(pk+" = ?", pkVal).Update(data)
	return err
}

// Delete 删除记录，软删除模型会自动改写为更新时间戳。
// 触发 before_delete/after_delete 事件。
func (m *Model) Delete() error {
	_, err := m.newModelQuery().Delete()
	return err
}

// ForceDelete 强制物理删除。
// 触发 before_delete/after_delete 事件。
func (m *Model) ForceDelete() error {
	_, err := m.newModelQuery().ForceDelete()
	return err
}

// Restore 恢复软删除记录。
func (m *Model) Restore() error {
	_, err := m.newModelQuery().Restore()
	return err
}

// HasOne 立即查询一对一关联。
func (m *Model) HasOne(relatedModel *Model, foreignKey string, localKey interface{}) (map[string]interface{}, error) {
	if err := validateImmediateRelation(m, relatedModel, foreignKey); err != nil {
		return nil, err
	}
	return relatedModel.Where(foreignKey+" = ?", localKey).Find()
}

// HasMany 立即查询一对多关联。
func (m *Model) HasMany(relatedModel *Model, foreignKey string, localKey interface{}) ([]map[string]interface{}, error) {
	if err := validateImmediateRelation(m, relatedModel, foreignKey); err != nil {
		return nil, err
	}
	return relatedModel.Where(foreignKey+" = ?", localKey).Select()
}

// BelongsTo 立即查询反向关联。
func (m *Model) BelongsTo(relatedModel *Model, foreignKey interface{}, ownerKey string) (map[string]interface{}, error) {
	if err := validateImmediateRelation(m, relatedModel, ownerKey); err != nil {
		return nil, err
	}
	return relatedModel.Where(ownerKey+" = ?", foreignKey).Find()
}

// structToMap 把结构体转换为列名→值的映射。
// 支持 `thinkgo` 标签：
//   - `thinkgo:"-"`          跳过该字段；
//   - `thinkgo:"col"`        指定列名；
//   - `thinkgo:"col,omitempty"` 当字段为零值时跳过，避免用零值覆盖已有数据。
func (m *Model) structToMap(v interface{}) (map[string]interface{}, error) {
	value := reflect.ValueOf(v)
	if !value.IsValid() {
		return nil, fmt.Errorf("%w: 结构体不能为空", ErrInvalidModel)
	}
	if value.Kind() == reflect.Ptr && !value.IsNil() {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%w: 期望结构体，实际为 %s", ErrInvalidModel, value.Kind())
	}

	metadata, err := loadModelMetadata(value.Type())
	if err != nil {
		return nil, err
	}
	data := make(map[string]interface{}, len(metadata.fields))
	for _, field := range metadata.fields {
		fieldValue := value.Field(field.index)
		if field.omitEmpty && fieldValue.IsZero() {
			continue
		}
		if !fieldValue.CanInterface() {
			continue
		}
		data[field.column] = fieldValue.Interface()
	}
	return data, nil
}

func writableModelStruct(value interface{}) (reflect.Value, error) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || reflected.Kind() != reflect.Ptr || reflected.IsNil() {
		return reflect.Value{}, fmt.Errorf("%w: Create/Update 要求非 nil 结构体指针", ErrInvalidModel)
	}
	reflected = reflected.Elem()
	if reflected.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("%w: Create/Update 要求结构体指针", ErrInvalidModel)
	}
	return reflected, nil
}

// parseStructTag 解析 thinkgo 标签，返回列名和选项集合。
func parseStructTag(tag string) (string, map[string]bool) {
	options := make(map[string]bool)
	if tag == "" {
		return "", options
	}
	parts := strings.Split(tag, ",")
	name := strings.TrimSpace(parts[0])
	for _, opt := range parts[1:] {
		opt = strings.TrimSpace(opt)
		if opt != "" {
			options[opt] = true
		}
	}
	return name, options
}

// isZeroDBValue 判断从结构体取出的值是否为该类型的零值。
func isZeroDBValue(value interface{}) bool {
	if value == nil {
		return true
	}
	return reflect.ValueOf(value).IsZero()
}

// ToSnakeCase 把驼峰命名转换成下划线格式。
func ToSnakeCase(s string) string {
	if s == "" {
		return s
	}

	runes := []rune(s)
	result := make([]rune, 0, len(runes))
	for index, current := range runes {
		if current >= 'A' && current <= 'Z' {
			if index > 0 {
				prev := runes[index-1]
				if (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z' && index+1 < len(runes) && runes[index+1] >= 'a' && runes[index+1] <= 'z') {
					result = append(result, '_')
				}
			}
			result = append(result, current+32)
			continue
		}
		result = append(result, current)
	}
	return string(result)
}

func (m *Model) setTimestamp(data map[string]interface{}, field string, now time.Time) error {
	valueType := TimestampValueTypeUnix
	if m != nil {
		m.mu.RLock()
		configured := m.timestampValueType
		m.mu.RUnlock()
		if configured != "" {
			valueType = configured
		}
	}
	return setAutoTimestamp(data, field, now, valueType)
}
