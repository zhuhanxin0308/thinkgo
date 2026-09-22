package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const (
	schemaCacheVersion = 1
	// MaxSchemaCacheBytes 限制单个连接结构缓存的最大大小。
	MaxSchemaCacheBytes = 16 << 20
)

// SchemaColumn 是来自数据库系统表的字段定义。
type SchemaColumn struct {
	Name          string  `json:"name"`
	DataType      string  `json:"type"`
	Nullable      bool    `json:"nullable"`
	PrimaryKey    bool    `json:"primary_key"`
	AutoIncrement bool    `json:"auto_increment"`
	DefaultValue  *string `json:"default,omitempty"`
}

// TableSchema 保存真实表名和数据库定义顺序一致的字段清单。
type TableSchema struct {
	Table   string         `json:"table"`
	Columns []SchemaColumn `json:"columns"`
}

type schemaCacheEnvelope struct {
	Version  int           `json:"version"`
	Identity string        `json:"identity"`
	Dialect  string        `json:"dialect"`
	Tables   []TableSchema `json:"tables"`
}

// SchemaCacheIdentity 绑定缓存与连接配置，仅返回摘要，缓存文件不包含连接口令。
func SchemaCacheIdentity(configuration Config) string {
	// 只选取决定数据库及命名空间的属性，密码、连接池和日志选项不参与结构身份。
	identity := struct {
		Type, Hostname, Hostport, Database, Username, Charset, Prefix string
		Params                                                        map[string]string
	}{
		Type: configuration.Type, Hostname: configuration.Hostname, Hostport: configuration.Hostport,
		Database: configuration.Database, Username: configuration.Username, Charset: configuration.Charset,
		Prefix: configuration.Prefix, Params: configuration.Params,
	}
	content, _ := json.Marshal(identity)
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// GetSchemaInfo 读取关系型结构信息；refresh 明确要求重新访问数据库系统表。
func (database *DB) GetSchemaInfo(ctx context.Context, table string, refresh bool) (TableSchema, error) {
	var result TableSchema
	if ctx == nil {
		return result, ErrInvalidDatabaseContext
	}
	ctx, cancel := databaseOperationContext(ctx)
	defer cancel()
	err := database.WithConnection(func(connection Connection) error {
		sqlConnection, ok := connection.(*SQLConnection)
		if !ok {
			return fmt.Errorf("%w: 当前连接不支持关系型结构信息", ErrInvalidQuery)
		}
		var err error
		result, err = sqlConnection.getSchemaInfo(ctx, table, refresh)
		return err
	})
	return result, err
}

func (connection *SQLConnection) getSchemaInfo(ctx context.Context, table string, refresh bool) (TableSchema, error) {
	if err := ctx.Err(); err != nil {
		return TableSchema{}, err
	}
	if _, _, err := splitSchemaTable(table); err != nil {
		return TableSchema{}, err
	}
	if !refresh {
		connection.schemaMu.RLock()
		cached, found := connection.schemaCache[table]
		connection.schemaMu.RUnlock()
		if found {
			return cloneTableSchema(cached), nil
		}
	}
	query, args, err := schemaCatalogQuery(connection.Builder.DialectName(), table)
	if err != nil {
		return TableSchema{}, err
	}
	rows, err := connection.QueryContext(ctx, query, args...)
	if err != nil {
		return TableSchema{}, err
	}
	result := TableSchema{Table: table, Columns: make([]SchemaColumn, 0, len(rows))}
	for _, row := range rows {
		column := SchemaColumn{Name: schemaValueText(row["name"]), DataType: schemaValueText(row["data_type"]), Nullable: schemaValueBool(row["nullable"]), PrimaryKey: schemaValueBool(row["primary_key"]), AutoIncrement: schemaValueBool(row["auto_increment"]) || strings.Contains(schemaValueText(row["extra"]), "auto_increment")}
		if value, exists := row["not_null"]; exists {
			column.Nullable = !schemaValueBool(value)
		}
		if value := row["default_value"]; value != nil {
			text := schemaValueText(value)
			column.DefaultValue = &text
		}
		result.Columns = append(result.Columns, column)
	}
	if err := validateTableSchema(result); err != nil {
		return TableSchema{}, err
	}
	connection.schemaMu.Lock()
	if connection.schemaCache == nil {
		connection.schemaCache = make(map[string]TableSchema)
	}
	connection.schemaCache[table] = cloneTableSchema(result)
	connection.schemaMu.Unlock()
	return result, nil
}

// SchemaTables 枚举指定数据库或模式的数据表，供 optimize:schema --table=* 使用。
func (database *DB) SchemaTables(ctx context.Context, namespace string) ([]string, error) {
	query, args, err := schemaTablesQuery(database.DialectName(), namespace)
	if err != nil {
		return nil, err
	}
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		name := schemaValueText(row["name"])
		if namespace != "" {
			name = namespace + "." + name
		}
		if _, _, err := splitSchemaTable(name); err != nil {
			return nil, err
		}
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

// ExportSchemaCache 从真实数据库刷新目标表后生成可跨进程消费的结构缓存。
func (database *DB) ExportSchemaCache(ctx context.Context, identity string, tables []string) ([]byte, error) {
	if identity == "" {
		return nil, fmt.Errorf("结构缓存缺少数据库身份")
	}
	cache := schemaCacheEnvelope{Version: schemaCacheVersion, Identity: identity, Dialect: database.DialectName(), Tables: make([]TableSchema, 0, len(tables))}
	seen := make(map[string]bool)
	for _, table := range tables {
		if seen[table] {
			continue
		}
		seen[table] = true
		info, err := database.GetSchemaInfo(ctx, table, true)
		if err != nil {
			return nil, err
		}
		cache.Tables = append(cache.Tables, info)
	}
	sort.Slice(cache.Tables, func(left, right int) bool { return cache.Tables[left].Table < cache.Tables[right].Table })
	content, err := json.MarshalIndent(cache, "", "    ")
	if err != nil {
		return nil, err
	}
	if len(content) >= MaxSchemaCacheBytes {
		return nil, fmt.Errorf("数据库结构缓存超过大小限制")
	}
	return append(content, '\n'), nil
}

// LoadSchemaCache 校验数据库身份和全部字段后原子安装缓存，不接受其他连接的产物。
func (database *DB) LoadSchemaCache(content []byte, identity string) error {
	if len(content) > MaxSchemaCacheBytes {
		return fmt.Errorf("数据库结构缓存超过大小限制")
	}
	var cache schemaCacheEnvelope
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cache); err != nil {
		return err
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return fmt.Errorf("数据库结构缓存存在尾随内容")
	}
	if cache.Version != schemaCacheVersion || cache.Identity != identity || identity == "" || cache.Dialect != database.DialectName() || cache.Tables == nil {
		return fmt.Errorf("数据库结构缓存身份、方言或版本不匹配")
	}
	tables := make(map[string]TableSchema, len(cache.Tables))
	for _, table := range cache.Tables {
		if _, exists := tables[table.Table]; exists {
			return fmt.Errorf("数据库结构缓存包含重复表")
		}
		if err := validateTableSchema(table); err != nil {
			return err
		}
		tables[table.Table] = cloneTableSchema(table)
	}
	return database.WithConnection(func(connection Connection) error {
		sqlConnection, ok := connection.(*SQLConnection)
		if !ok {
			return fmt.Errorf("%w: 当前连接不支持结构缓存", ErrInvalidQuery)
		}
		sqlConnection.schemaMu.Lock()
		sqlConnection.schemaCache = tables
		sqlConnection.schemaMu.Unlock()
		return nil
	})
}

func validateTableSchema(table TableSchema) error {
	if _, _, err := splitSchemaTable(table.Table); err != nil {
		return err
	}
	if len(table.Columns) == 0 {
		return fmt.Errorf("数据表 %q 不存在或没有可读取字段", table.Table)
	}
	seen := make(map[string]bool)
	for _, column := range table.Columns {
		if column.Name == "" || strings.ContainsAny(column.Name, ".* /\\-()\"'`") || seen[column.Name] {
			return fmt.Errorf("数据表字段名称为空或重复")
		}
		if err := validateIdentifier(column.Name); err != nil {
			return err
		}
		seen[column.Name] = true
	}
	return nil
}

func cloneTableSchema(table TableSchema) TableSchema {
	table.Columns = append([]SchemaColumn(nil), table.Columns...)
	for index := range table.Columns {
		if table.Columns[index].DefaultValue != nil {
			value := *table.Columns[index].DefaultValue
			table.Columns[index].DefaultValue = &value
		}
	}
	return table
}

func (connection *SQLConnection) cachedSchemaFields(table string) string {
	connection.schemaMu.RLock()
	cached, exists := connection.schemaCache[table]
	connection.schemaMu.RUnlock()
	if !exists {
		return ""
	}
	fields := make([]string, len(cached.Columns))
	for index, column := range cached.Columns {
		fields[index] = column.Name
	}
	return strings.Join(fields, ",")
}

func schemaValueText(value interface{}) string {
	if value == nil {
		return ""
	}
	if binary, ok := value.([]byte); ok {
		return string(binary)
	}
	return fmt.Sprint(value)
}

func schemaValueBool(value interface{}) bool {
	if number, err := strconv.ParseInt(schemaValueText(value), 10, 64); err == nil {
		return number > 0
	}
	switch strings.ToUpper(schemaValueText(value)) {
	case "1", "TRUE", "YES", "Y", "PRI":
		return true
	default:
		return false
	}
}
