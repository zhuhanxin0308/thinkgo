package db

import (
	"fmt"
	"reflect"
	"strings"
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
type ModelSearcherFunc func(query *Query, value interface{}, data map[string]interface{})

// Model 表示一个数据库模型，支持时间戳、软删除、关系定义、模型事件、获取器/修改器/搜索器。
type Model struct {
	table              string
	db                 *DB
	autoTimestamp      bool
	createTimeField    string
	updateTimeField    string
	timestampValueType string
	softDelete         bool
	deleteTimeField    string
	primaryKey         string
	relations          map[string]RelationDefinition
	events             map[ModelEventType][]ModelEventCallback // 模型事件回调
	getters            map[string]ModelGetterFunc             // 获取器映射
	setters            map[string]ModelSetterFunc             // 修改器映射
	searchers          map[string]ModelSearcherFunc           // 搜索器映射
}

// NewModel 创建模型实例。
func NewModel(db *DB, table string) *Model {
	return &Model{
		db:                 db,
		table:              table,
		autoTimestamp:      db.autoTimestamp,
		createTimeField:    db.createTimeField,
		updateTimeField:    db.updateTimeField,
		timestampValueType: db.timestampValueType,
		primaryKey:         "id",
		relations:          make(map[string]RelationDefinition),
		events:             make(map[ModelEventType][]ModelEventCallback),
		getters:            make(map[string]ModelGetterFunc),
		setters:            make(map[string]ModelSetterFunc),
		searchers:          make(map[string]ModelSearcherFunc),
	}
}

// Getter 注册字段获取器。
// 查询结果返回时，自动对指定字段应用获取器转换。
// 对应 ThinkPHP 的 getFieldNameAttr 方法。
// 示例：model.Getter("status", func(v interface{}, data map[string]interface{}) interface{} {
//     if v == 1 { return "启用" }
//     return "禁用"
// })
func (m *Model) Getter(field string, fn ModelGetterFunc) *Model {
	m.getters[field] = fn
	return m
}

// Setter 注册字段修改器。
// 插入或更新数据时，自动对指定字段应用修改器转换。
// 对应 ThinkPHP 的 setFieldNameAttr 方法。
// 示例：model.Setter("password", func(v interface{}, data map[string]interface{}) interface{} {
//     return md5(v.(string))
// })
func (m *Model) Setter(field string, fn ModelSetterFunc) *Model {
	m.setters[field] = fn
	return m
}

// Searcher 注册字段搜索器。
// 使用 WithSearch 时，自动将搜索条件映射为查询条件。
// 对应 ThinkPHP 的 searchFieldNameAttr 方法。
// 示例：model.Searcher("name", func(q *Query, v interface{}, data map[string]interface{}) {
//     q.Where("name LIKE ?", "%"+v.(string)+"%")
// })
func (m *Model) Searcher(field string, fn ModelSearcherFunc) *Model {
	m.searchers[field] = fn
	return m
}

// applyGetters 对查询结果行应用获取器转换。
func (m *Model) applyGetters(row map[string]interface{}) map[string]interface{} {
	if len(m.getters) == 0 || row == nil {
		return row
	}
	for field, getter := range m.getters {
		if val, ok := row[field]; ok {
			row[field] = getter(val, row)
		}
	}
	return row
}

// applySetters 对写入数据应用修改器转换。
func (m *Model) applySetters(data map[string]interface{}) map[string]interface{} {
	if len(m.setters) == 0 || data == nil {
		return data
	}
	for field, setter := range m.setters {
		if val, ok := data[field]; ok {
			data[field] = setter(val, data)
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
	for _, field := range fields {
		searcher, ok := m.searchers[field]
		if !ok {
			continue
		}
		value, exists := data[field]
		if !exists {
			continue
		}
		searcher(mq.query, value, data)
	}
	return mq
}

// On 注册模型事件回调。
// 对应 ThinkPHP 的 Model::event() 静态方法。
func (m *Model) On(eventType ModelEventType, callback ModelEventCallback) *Model {
	m.events[eventType] = append(m.events[eventType], callback)
	return m
}

// fireEvent 触发模型事件。
// before_* 事件中任一回调返回 false 将阻止操作。
func (m *Model) fireEvent(eventType ModelEventType, data map[string]interface{}) bool {
	callbacks, ok := m.events[eventType]
	if !ok {
		return true
	}
	for _, cb := range callbacks {
		if !cb(data) {
			return false
		}
	}
	return true
}

// NewModelAuto 根据结构体名称自动推断表名。
// 默认规则仅做驼峰转下划线和小写化，不再自动复数化。
func NewModelAuto(db *DB, model interface{}) *Model {
	return NewModel(db, GetTableName(model))
}

// GetTableName 从结构体类型推断表名。
// 表前缀由 DB 配置统一追加，这里只负责生成不带前缀的小写表名。
func GetTableName(model interface{}) string {
	t := reflect.TypeOf(model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return ToSnakeCase(t.Name())
}

// SetDB 设置数据库连接。
func (m *Model) SetDB(db *DB) {
	m.db = db
}

// Table 设置表名。
func (m *Model) Table(name string) *Model {
	m.table = name
	return m
}

// AutoTimestamp 设置是否自动维护时间戳。
func (m *Model) AutoTimestamp(enable bool) *Model {
	m.autoTimestamp = enable
	return m
}

// CreateTimeField 设置创建时间字段。
func (m *Model) CreateTimeField(name string) *Model {
	m.createTimeField = name
	return m
}

// UpdateTimeField 设置更新时间字段。
func (m *Model) UpdateTimeField(name string) *Model {
	m.updateTimeField = name
	return m
}

// PrimaryKey 设置主键字段名（默认 "id"）。
func (m *Model) PrimaryKey(name string) *Model {
	if name != "" {
		m.primaryKey = name
	}
	return m
}

// primaryKeyField 返回主键字段名，未配置时回退到 "id"。
func (m *Model) primaryKeyField() string {
	if m.primaryKey == "" {
		return "id"
	}
	return m.primaryKey
}

// SoftDelete 开启软删除。
func (m *Model) SoftDelete(deleteField ...string) *Model {
	m.softDelete = true
	m.deleteTimeField = "delete_time"
	if len(deleteField) > 0 && deleteField[0] != "" {
		m.deleteTimeField = deleteField[0]
	}
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
func (m *Model) DefineHasOne(name string, relatedModel *Model, foreignKey string, localKey string) *Model {
	m.ensureRelations()
	m.relations[name] = RelationDefinition{
		Name:       name,
		Type:       relationHasOne,
		Related:    relatedModel,
		ForeignKey: foreignKey,
		LocalKey:   localKey,
	}
	return m
}

// DefineHasMany 定义一对多关联。
func (m *Model) DefineHasMany(name string, relatedModel *Model, foreignKey string, localKey string) *Model {
	m.ensureRelations()
	m.relations[name] = RelationDefinition{
		Name:       name,
		Type:       relationHasMany,
		Related:    relatedModel,
		ForeignKey: foreignKey,
		LocalKey:   localKey,
	}
	return m
}

// DefineBelongsTo 定义反向关联。
func (m *Model) DefineBelongsTo(name string, relatedModel *Model, foreignKey string, ownerKey string) *Model {
	m.ensureRelations()
	m.relations[name] = RelationDefinition{
		Name:       name,
		Type:       relationBelongsTo,
		Related:    relatedModel,
		ForeignKey: foreignKey,
		OwnerKey:   ownerKey,
	}
	return m
}

// DefineBelongsToMany 定义多对多关联。
// pivotTable: 中间表名（不含前缀，框架会自动拼接）
// foreignKey: 当前模型在中间表中的外键字段名
// relatedForeignKey: 关联模型在中间表中的外键字段名
// localKey: 当前模型的主键字段名
// 对应 ThinkPHP 的 $this->belongsToMany(Role::class, 'user_role', 'role_id', 'user_id')
func (m *Model) DefineBelongsToMany(name string, relatedModel *Model, pivotTable string, foreignKey string, relatedForeignKey string, localKey string) *Model {
	m.ensureRelations()
	m.relations[name] = RelationDefinition{
		Name:              name,
		Type:              relationBelongsToMany,
		Related:           relatedModel,
		ForeignKey:        foreignKey,
		LocalKey:          localKey,
		PivotTable:        pivotTable,
		RelatedForeignKey: relatedForeignKey,
	}
	return m
}

// BelongsToMany 立即查询多对多关联。
// 通过中间表进行两次查询：先查中间表取关联 ID，再查关联表取数据。
func (m *Model) BelongsToMany(relatedModel *Model, pivotTable string, foreignKey string, relatedForeignKey string, localKeyValue interface{}) ([]map[string]interface{}, error) {
	// 1. 查询中间表，获取关联模型的 ID 列表
	pivotRows, err := m.db.Name(pivotTable).Where(foreignKey+" = ?", localKeyValue).Select()
	if err != nil {
		return nil, fmt.Errorf("查询中间表 %s 失败: %w", pivotTable, err)
	}
	if len(pivotRows) == 0 {
		return []map[string]interface{}{}, nil
	}

	// 2. 提取关联模型 ID
	relatedKeys := make([]interface{}, 0, len(pivotRows))
	for _, row := range pivotRows {
		if val, ok := row[relatedForeignKey]; ok {
			relatedKeys = append(relatedKeys, val)
		}
	}
	if len(relatedKeys) == 0 {
		return []map[string]interface{}{}, nil
	}

	// 3. 查询关联模型
	return relatedModel.query().WhereIn(relatedModel.primaryKeyField(), relatedKeys).Select()
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

// Count 统计记录总数。
func (m *Model) Count() (int64, error) {
	return m.newModelQuery().Count()
}

// Insert 使用 map 插入记录。
// 自动应用修改器，触发 before_insert/after_insert 模型事件。
func (m *Model) Insert(data map[string]interface{}) (int64, error) {
	return m.newModelQuery().Insert(data)
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
	data := m.structToMap(v)

	pk := m.primaryKeyField()
	if val, ok := data[pk]; ok && isZeroDBValue(val) {
		delete(data, pk)
	}

	if m.autoTimestamp {
		now := time.Now()
		m.setTimestamp(data, m.createTimeField, now)
		m.setTimestamp(data, m.updateTimeField, now)
	}

	id, err := m.newModelQuery().Insert(data)
	if err != nil {
		return err
	}

	value := reflect.ValueOf(v)
	if value.Kind() == reflect.Ptr {
		value = value.Elem()
	}
	if value.Kind() == reflect.Struct {
		idField := value.FieldByName("ID")
		if idField.IsValid() && idField.CanSet() && idField.Kind() == reflect.Int64 {
			idField.SetInt(id)
		}
	}
	return nil
}

// Update 从结构体更新记录。
// 以主键作为 WHERE 条件按行更新，并将主键从 SET 子句中剔除，
// 避免无 WHERE 的全表更新和对主键的误写。
// 自动应用修改器并触发 before_update/after_update 事件。
func (m *Model) Update(v interface{}) error {
	data := m.structToMap(v)

	pk := m.primaryKeyField()
	pkVal, ok := data[pk]
	if !ok || isZeroDBValue(pkVal) {
		return fmt.Errorf("结构体更新需要非零主键 %q 作为更新条件", pk)
	}
	delete(data, pk)

	if m.autoTimestamp {
		m.setTimestamp(data, m.updateTimeField, time.Now())
	}
	_, err := m.newModelQuery().Where(pk+" = ?", pkVal).Update(data)
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
	return relatedModel.Where(foreignKey+" = ?", localKey).Find()
}

// HasMany 立即查询一对多关联。
func (m *Model) HasMany(relatedModel *Model, foreignKey string, localKey interface{}) ([]map[string]interface{}, error) {
	return relatedModel.Where(foreignKey+" = ?", localKey).Select()
}

// BelongsTo 立即查询反向关联。
func (m *Model) BelongsTo(relatedModel *Model, foreignKey interface{}, ownerKey string) (map[string]interface{}, error) {
	return relatedModel.Where(ownerKey+" = ?", foreignKey).Find()
}



func collectRelationKeys(rows []map[string]interface{}, field string) []interface{} {
	seen := make(map[string]bool)
	keys := make([]interface{}, 0)
	for _, row := range rows {
		value, ok := row[field]
		if !ok {
			continue
		}
		text := fmt.Sprint(value)
		if seen[text] {
			continue
		}
		seen[text] = true
		keys = append(keys, value)
	}
	return keys
}

func groupRelationRows(rows []map[string]interface{}, field string) map[string][]map[string]interface{} {
	grouped := make(map[string][]map[string]interface{})
	for _, row := range rows {
		grouped[fmt.Sprint(row[field])] = append(grouped[fmt.Sprint(row[field])], row)
	}
	return grouped
}

func indexRelationRows(rows []map[string]interface{}, field string) map[string]map[string]interface{} {
	indexed := make(map[string]map[string]interface{})
	for _, row := range rows {
		indexed[fmt.Sprint(row[field])] = row
	}
	return indexed
}

func (m *Model) ensureRelations() {
	if m.relations == nil {
		m.relations = make(map[string]RelationDefinition)
	}
}

// structToMap 把结构体转换为列名→值的映射。
// 支持 `thinkgo` 标签：
//   - `thinkgo:"-"`          跳过该字段；
//   - `thinkgo:"col"`        指定列名；
//   - `thinkgo:"col,omitempty"` 当字段为零值时跳过，避免用零值覆盖已有数据。
func (m *Model) structToMap(v interface{}) map[string]interface{} {
	data := make(map[string]interface{})
	value := reflect.ValueOf(v)
	if value.Kind() == reflect.Ptr {
		value = value.Elem()
	}

	typ := value.Type()
	for index := 0; index < value.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath != "" {
			continue
		}

		tag := field.Tag.Get("thinkgo")
		name, options := parseStructTag(tag)
		if name == "-" {
			continue
		}
		if name == "" {
			name = ToSnakeCase(field.Name)
		}

		fieldValue := value.Field(index)
		if options["omitempty"] && fieldValue.IsZero() {
			continue
		}
		data[name] = fieldValue.Interface()
	}
	return data
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

func (m *Model) setTimestamp(data map[string]interface{}, field string, now time.Time) {
	valueType := TimestampValueTypeUnix
	if m != nil && m.timestampValueType != "" {
		valueType = m.timestampValueType
	}
	setAutoTimestamp(data, field, now, valueType)
}
