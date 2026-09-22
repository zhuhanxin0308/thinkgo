package neo4j

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const (
	defaultNeo4jOpTimeout = 10 * time.Second
	maxCypherLikeLength   = 4096
)

// Neo4jConnection 将统一查询接口映射为参数化 Cypher。
type Neo4jConnection struct {
	Driver           neo4j.DriverWithContext
	Database         string
	OperationTimeout time.Duration
	closeOnce        sync.Once
	closeErr         error
	executor         neo4jOperationExecutor
	locationMu       sync.RWMutex
	location         *time.Location
	identity         ConnectionID
	identityOnce     sync.Once
}

func (c *Neo4jConnection) ConnectionID() ConnectionID {
	if c == nil {
		return ""
	}
	c.identityOnce.Do(func() { c.identity = NewConnectionID("neo4j") })
	return c.identity
}

var (
	_ Connection              = (*Neo4jConnection)(nil)
	_ LocationAwareConnection = (*Neo4jConnection)(nil)
	_ CapabilityProvider      = (*Neo4jConnection)(nil)
)

func (c *Neo4jConnection) Capabilities() DriverCapabilities {
	return DriverCapabilities{
		InsertIDKinds:      []InsertIDKind{InsertIDString, InsertIDInteger, InsertIDDynamic},
		MatchedCountKnown:  true,
		ModifiedCountKnown: false,
	}
}

// SetLocation 设置 Neo4j 原生时间参数和查询结果使用的应用时区。
func (c *Neo4jConnection) SetLocation(location *time.Location) {
	if c == nil {
		return
	}
	if location == nil {
		location = time.UTC
	}
	c.locationMu.Lock()
	c.location = location
	c.locationMu.Unlock()
}

// Location 返回 Neo4j 连接使用的应用时区。
func (c *Neo4jConnection) Location() *time.Location {
	if c == nil {
		return time.UTC
	}
	c.locationMu.RLock()
	location := c.location
	c.locationMu.RUnlock()
	if location == nil {
		return time.UTC
	}
	return location
}

func (c *Neo4jConnection) Select(parent context.Context, request SelectRequest) ([]map[string]interface{}, error) {
	if request.Aggregate() != nil {
		return nil, fmt.Errorf("%w: Neo4j aggregate expressions require an explicit Cypher projection", ErrInvalidQuery)
	}
	return c.selectRequest(parent, request.Table(), request.Fields(), request.Predicate(), request.Order(), request.Limit(), request.Offset())
}

func (c *Neo4jConnection) Insert(parent context.Context, request InsertRequest) (InsertResult, error) {
	data := request.Data()
	var id interface{}
	if request.WantsID() {
		var exists bool
		id, exists = data[request.PrimaryKey()]
		if !exists || isZeroDBValue(id) {
			return InsertResult{}, ErrInsertIDUnavailable
		}
	}
	affected, err := c.createNode(parent, request.Table(), data)
	if err != nil {
		return InsertResult{}, err
	}
	operationResult := InsertResult{Affected: affected, Data: data}
	if request.WantsID() {
		operationResult.ID = id
		operationResult.IDKnown = true
	}
	return operationResult, operationResult.Validate()
}

func (c *Neo4jConnection) Update(parent context.Context, request UpdateRequest) (UpdateResult, error) {
	matched, err := c.updateNodes(parent, request.Table(), request.Data(), request.Predicate())
	if err != nil {
		return UpdateResult{}, err
	}
	operationResult := UpdateResult{
		Affected:     matched,
		Matched:      matched,
		MatchedKnown: true,
		Data:         request.Data(),
	}
	return operationResult, operationResult.Validate()
}

func (c *Neo4jConnection) Delete(parent context.Context, request DeleteRequest) (DeleteResult, error) {
	result, err := c.deleteNodes(parent, request.Table(), request.Predicate(), request.DetachRelations())
	if err != nil {
		return DeleteResult{}, err
	}
	return result, result.Validate()
}

func (c *Neo4jConnection) Count(parent context.Context, request CountRequest) (int64, error) {
	return c.countNodes(parent, request.Table(), request.Predicate())
}

func (c *Neo4jConnection) validate() error {
	if c == nil || (c.Driver == nil && c.executor == nil) {
		return ErrDatabaseUnavailable
	}
	return nil
}

func (c *Neo4jConnection) operationContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	if err := c.validate(); err != nil {
		return nil, nil, err
	}
	if parent == nil {
		return nil, nil, fmt.Errorf("%w: Neo4j 上下文不能为空", ErrInvalidQuery)
	}
	timeout := c.OperationTimeout
	if timeout <= 0 {
		timeout = defaultNeo4jOpTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	return ctx, cancel, nil
}

func (c *Neo4jConnection) operationExecutor() neo4jOperationExecutor {
	if c.executor != nil {
		return c.executor
	}
	return &neo4jDriverExecutor{driver: c.Driver, database: c.Database}
}

func cypherIdentifier(name string) (string, error) {
	name = strings.TrimSpace(name)
	if strings.Contains(name, ".") {
		return "", fmt.Errorf("%w: Neo4j 标识符不支持点分层级 %q", ErrInvalidQuery, name)
	}
	if err := validateIdentifier(name); err != nil {
		return "", fmt.Errorf("%w: 非法 Neo4j 标识符 %q: %w", ErrInvalidQuery, name, err)
	}
	return "`" + name + "`", nil
}

func validateNeo4jProperties(data map[string]interface{}) error {
	for key := range data {
		if _, err := cypherIdentifier(key); err != nil {
			return fmt.Errorf("%w: 非法 Neo4j 属性 %q: %v", ErrInvalidQuery, key, err)
		}
	}
	return nil
}

func (c *Neo4jConnection) selectRequest(parent context.Context, table, fields string, predicate Predicate, order string, limit, offset int) ([]map[string]interface{}, error) {
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if limit < 0 || offset < 0 {
		return nil, ErrInvalidPagination
	}
	label, err := cypherIdentifier(table)
	if err != nil {
		return nil, err
	}
	cypher := fmt.Sprintf("MATCH (n:%s)", label)
	whereClause, params, err := c.compilePredicate(predicate)
	if err != nil {
		return nil, err
	}
	if whereClause != "" {
		cypher += " WHERE " + whereClause
	}
	projected := fields != "" && fields != "*"
	if projected {
		projections := make([]string, 0)
		aliases := make(map[string]struct{})
		for _, rawField := range strings.Split(fields, ",") {
			field, alias, err := parseCypherProjection(rawField)
			if err != nil {
				return nil, err
			}
			if _, duplicated := aliases[alias]; duplicated {
				return nil, fmt.Errorf("%w: Neo4j 投影别名 %s 重复", ErrInvalidQuery, alias)
			}
			aliases[alias] = struct{}{}
			projections = append(projections, fmt.Sprintf("n.%s AS %s", field, alias))
		}
		cypher += " RETURN " + strings.Join(projections, ", ")
	} else {
		cypher += " RETURN n"
	}
	if order != "" {
		if err := validateOrderClause(order); err != nil {
			return nil, fmt.Errorf("%w: 非法 Neo4j 排序: %w", ErrInvalidQuery, err)
		}
		parts := make([]string, 0)
		for _, rawPart := range strings.Split(order, ",") {
			items := strings.Fields(rawPart)
			field, err := cypherIdentifier(items[0])
			if err != nil {
				return nil, err
			}
			direction := ""
			if len(items) == 2 {
				direction = " " + strings.ToUpper(items[1])
			}
			parts = append(parts, "n."+field+direction)
		}
		cypher += " ORDER BY " + strings.Join(parts, ", ")
	}
	if offset > 0 {
		cypher += fmt.Sprintf(" SKIP %d", offset)
	}
	if limit > 0 {
		cypher += fmt.Sprintf(" LIMIT %d", limit)
	}

	records, err := c.operationExecutor().Collect(ctx, neo4j.AccessModeRead, cypher, params)
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]interface{}, 0, len(records))
	for index, record := range records {
		if projected {
			row := make(map[string]interface{}, len(record.Keys))
			seenKeys := make(map[string]struct{}, len(record.Keys))
			for _, key := range record.Keys {
				if _, duplicated := seenKeys[key]; duplicated {
					return nil, fmt.Errorf("%w: Neo4j 第 %d 行包含重复字段 %q", ErrInvalidDatabaseRow, index, key)
				}
				seenKeys[key] = struct{}{}
				value, exists := record.Get(key)
				if !exists {
					return nil, fmt.Errorf("%w: Neo4j 第 %d 行缺少字段 %q", ErrInvalidDatabaseRow, index, key)
				}
				row[key] = normalizeNeo4jValueInLocation(value, c.Location())
			}
			rows = append(rows, row)
			continue
		}
		if len(record.Values) != 1 {
			return nil, fmt.Errorf("%w: Neo4j 节点查询列数错误", ErrInvalidDatabaseRow)
		}
		node, ok := record.Values[0].(neo4j.Node)
		if !ok {
			return nil, fmt.Errorf("%w: Neo4j 返回值不是节点", ErrInvalidDatabaseRow)
		}
		rows = append(rows, normalizeNeo4jMapInLocation(node.Props, c.Location()))
	}
	return rows, nil
}

// normalizeNeo4jMapInLocation 递归规范化节点属性中的时间值。
func normalizeNeo4jMapInLocation(source map[string]interface{}, location *time.Location) map[string]interface{} {
	if source == nil {
		return nil
	}
	result := make(map[string]interface{}, len(source))
	for key, value := range source {
		result[key] = normalizeNeo4jValueInLocation(value, location)
	}
	return result
}

// normalizeNeo4jValueInLocation 将 Neo4j 的有时区和无时区时间统一暴露为应用时区下的 time.Time。
func normalizeNeo4jValueInLocation(value interface{}, location *time.Location) interface{} {
	if location == nil {
		location = time.UTC
	}
	switch typed := value.(type) {
	case time.Time:
		return typed.In(location)
	case *time.Time:
		if typed == nil {
			return (*time.Time)(nil)
		}
		converted := typed.In(location)
		return &converted
	case neo4j.Date:
		return neo4jWallClockTime(time.Time(typed), location)
	case neo4j.LocalDateTime:
		return neo4jWallClockTime(time.Time(typed), location)
	case neo4j.Time:
		return time.Time(typed).In(location)
	case neo4j.LocalTime:
		return neo4jWallClockTime(time.Time(typed), location)
	case map[string]interface{}:
		return normalizeNeo4jMapInLocation(typed, location)
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			result[index] = normalizeNeo4jValueInLocation(item, location)
		}
		return result
	default:
		return value
	}
}

func neo4jWallClockTime(value time.Time, location *time.Location) time.Time {
	return time.Date(
		value.Year(), value.Month(), value.Day(),
		value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), location,
	)
}

func parseCypherProjection(raw string) (field, alias string, err error) {
	item := strings.TrimSpace(raw)
	aliasName := item
	upper := strings.ToUpper(item)
	if index := strings.Index(upper, " AS "); index >= 0 {
		aliasName = strings.TrimSpace(item[index+4:])
		item = strings.TrimSpace(item[:index])
	}
	field, err = cypherIdentifier(item)
	if err != nil {
		return "", "", err
	}
	alias, err = cypherIdentifier(aliasName)
	return field, alias, err
}

func (c *Neo4jConnection) createNode(parent context.Context, table string, data map[string]interface{}) (int64, error) {
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return 0, err
	}
	defer cancel()
	if len(data) == 0 {
		return 0, fmt.Errorf("%w: Neo4j 插入数据不能为空", ErrInvalidQuery)
	}
	if err := validateNeo4jProperties(data); err != nil {
		return 0, fmt.Errorf("%w: 非法 Neo4j 属性: %w", ErrInvalidQuery, err)
	}
	label, err := cypherIdentifier(table)
	if err != nil {
		return 0, err
	}
	record, err := c.operationExecutor().Single(ctx, neo4j.AccessModeWrite, fmt.Sprintf("CREATE (n:%s $props) RETURN count(n)", label), map[string]interface{}{"props": cloneDatabaseMap(data)})
	if err != nil {
		return 0, err
	}
	return cypherCount(record)
}

func (c *Neo4jConnection) updateNodes(parent context.Context, table string, data map[string]interface{}, predicate Predicate) (int64, error) {
	if predicate.Empty() {
		return 0, ErrUnsafeFullTableMutation
	}
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return 0, err
	}
	defer cancel()
	if len(data) == 0 {
		return 0, fmt.Errorf("%w: Neo4j 更新数据不能为空", ErrInvalidQuery)
	}
	if err := validateNeo4jProperties(data); err != nil {
		return 0, fmt.Errorf("%w: 非法 Neo4j 属性: %w", ErrInvalidQuery, err)
	}
	label, err := cypherIdentifier(table)
	if err != nil {
		return 0, err
	}
	whereClause, params, err := c.compilePredicate(predicate)
	if err != nil {
		return 0, err
	}
	params["props"] = cloneDatabaseMap(data)
	cypher := fmt.Sprintf("MATCH (n:%s) WHERE %s SET n += $props RETURN count(n)", label, whereClause)
	record, err := c.operationExecutor().Single(ctx, neo4j.AccessModeWrite, cypher, params)
	if err != nil {
		return 0, err
	}
	return cypherCount(record)
}

func (c *Neo4jConnection) deleteNodes(parent context.Context, table string, predicate Predicate, detachRelations bool) (DeleteResult, error) {
	if predicate.Empty() {
		return DeleteResult{}, ErrUnsafeFullTableMutation
	}
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return DeleteResult{}, err
	}
	defer cancel()
	label, err := cypherIdentifier(table)
	if err != nil {
		return DeleteResult{}, err
	}
	whereClause, params, err := c.compilePredicate(predicate)
	if err != nil {
		return DeleteResult{}, err
	}
	deleteClause := "DELETE n"
	if detachRelations {
		deleteClause = "DETACH DELETE n"
	}
	cypher := fmt.Sprintf("MATCH (n:%s) WHERE %s %s", label, whereClause, deleteClause)
	return c.operationExecutor().Execute(ctx, neo4j.AccessModeWrite, cypher, params)
}

func (c *Neo4jConnection) countNodes(parent context.Context, table string, predicate Predicate) (int64, error) {
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return 0, err
	}
	defer cancel()
	label, err := cypherIdentifier(table)
	if err != nil {
		return 0, err
	}
	whereClause, params, err := c.compilePredicate(predicate)
	if err != nil {
		return 0, err
	}
	cypher := fmt.Sprintf("MATCH (n:%s)", label)
	if whereClause != "" {
		cypher += " WHERE " + whereClause
	}
	cypher += " RETURN count(n)"
	record, err := c.operationExecutor().Single(ctx, neo4j.AccessModeRead, cypher, params)
	if err != nil {
		return 0, err
	}
	return cypherCount(record)
}

func cypherCount(record *neo4j.Record) (int64, error) {
	if record == nil || len(record.Values) != 1 {
		return 0, fmt.Errorf("%w: Neo4j 计数结果结构非法", ErrInvalidDatabaseRow)
	}
	count, ok := record.Values[0].(int64)
	if !ok || count < 0 {
		return 0, fmt.Errorf("%w: Neo4j 计数结果类型为 %T", ErrInvalidAggregateValue, record.Values[0])
	}
	return count, nil
}

func (c *Neo4jConnection) Close() error {
	if c == nil || (c.Driver == nil && c.executor == nil) {
		return ErrDatabaseUnavailable
	}
	c.closeOnce.Do(func() {
		timeout := c.OperationTimeout
		if timeout <= 0 {
			timeout = defaultNeo4jOpTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		c.closeErr = c.operationExecutor().Close(ctx)
	})
	return c.closeErr
}

func nextCypherParameter(index *int) string {
	name := fmt.Sprintf("w%d", *index)
	*index++
	return name
}

func cypherLikeRegex(value interface{}) (string, error) {
	var pattern string
	switch typed := value.(type) {
	case string:
		pattern = typed
	case []byte:
		pattern = string(typed)
	default:
		return "", fmt.Errorf("%w: Neo4j LIKE 参数必须是字符串", ErrInvalidQuery)
	}
	if len(pattern) > maxCypherLikeLength {
		return "", fmt.Errorf("%w: Neo4j LIKE 模式过长", ErrInvalidQuery)
	}
	var builder strings.Builder
	builder.WriteString("(?i)^")
	for _, character := range pattern {
		switch character {
		case '%':
			builder.WriteString(".*")
		case '_':
			builder.WriteByte('.')
		default:
			builder.WriteString(regexp.QuoteMeta(string(character)))
		}
	}
	builder.WriteByte('$')
	return builder.String(), nil
}
