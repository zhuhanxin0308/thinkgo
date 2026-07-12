package db

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

var cypherCollectionPlaceholders = regexp.MustCompile(`^\(\s*\?(?:\s*,\s*\?)*\s*\)$`)

// Neo4jConnection 将统一查询接口映射为参数化 Cypher。
type Neo4jConnection struct {
	Driver           neo4j.DriverWithContext
	Database         string
	OperationTimeout time.Duration
	closeOnce        sync.Once
	closeErr         error
	executor         neo4jOperationExecutor
}

var _ ContextualConnection = (*Neo4jConnection)(nil)

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

func (c *Neo4jConnection) Select(table, fields string, where []string, args []interface{}, order string, limit, offset int) ([]map[string]interface{}, error) {
	return c.SelectContext(context.Background(), table, fields, where, args, order, limit, offset)
}

func (c *Neo4jConnection) SelectContext(parent context.Context, table, fields string, where []string, args []interface{}, order string, limit, offset int) ([]map[string]interface{}, error) {
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
	whereClause, params, err := c.buildCypherWhere(where, args)
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
				row[key] = value
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
		rows = append(rows, cloneDatabaseMap(node.Props))
	}
	return rows, nil
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

func (c *Neo4jConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	return c.InsertContext(context.Background(), table, data)
}

func (c *Neo4jConnection) InsertContext(parent context.Context, table string, data map[string]interface{}) (int64, error) {
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return 0, err
	}
	defer cancel()
	if len(data) == 0 {
		return 0, fmt.Errorf("%w: Neo4j 插入数据不能为空", ErrInvalidQuery)
	}
	if err := validateDataKeys(data); err != nil {
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

func (c *Neo4jConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	return c.UpdateContext(context.Background(), table, data, where, args)
}

func (c *Neo4jConnection) UpdateContext(parent context.Context, table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	if len(where) == 0 {
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
	if err := validateDataKeys(data); err != nil {
		return 0, fmt.Errorf("%w: 非法 Neo4j 属性: %w", ErrInvalidQuery, err)
	}
	label, err := cypherIdentifier(table)
	if err != nil {
		return 0, err
	}
	whereClause, params, err := c.buildCypherWhere(where, args)
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

func (c *Neo4jConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	return c.DeleteContext(context.Background(), table, where, args)
}

func (c *Neo4jConnection) DeleteContext(parent context.Context, table string, where []string, args []interface{}) (int64, error) {
	if len(where) == 0 {
		return 0, ErrUnsafeFullTableMutation
	}
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return 0, err
	}
	defer cancel()
	label, err := cypherIdentifier(table)
	if err != nil {
		return 0, err
	}
	whereClause, params, err := c.buildCypherWhere(where, args)
	if err != nil {
		return 0, err
	}
	cypher := fmt.Sprintf("MATCH (n:%s) WHERE %s DETACH DELETE n", label, whereClause)
	return c.operationExecutor().Execute(ctx, neo4j.AccessModeWrite, cypher, params)
}

func (c *Neo4jConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	return c.CountContext(context.Background(), table, where, args)
}

func (c *Neo4jConnection) CountContext(parent context.Context, table string, where []string, args []interface{}) (int64, error) {
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return 0, err
	}
	defer cancel()
	label, err := cypherIdentifier(table)
	if err != nil {
		return 0, err
	}
	whereClause, params, err := c.buildCypherWhere(where, args)
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

func (c *Neo4jConnection) buildCypherWhere(where []string, args []interface{}) (string, map[string]interface{}, error) {
	params := make(map[string]interface{})
	clauses := make([]string, 0, len(where))
	argumentIndex := 0
	parameterIndex := 0
	for _, expression := range where {
		clause, err := c.parseCypherBoolean(expression, args, &argumentIndex, &parameterIndex, params)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, clause)
	}
	if argumentIndex != len(args) {
		return "", nil, fmt.Errorf("%w: Neo4j 条件使用 %d 个参数，实际传入 %d 个", ErrInvalidQuery, argumentIndex, len(args))
	}
	return strings.Join(clauses, " AND "), params, nil
}

func (c *Neo4jConnection) parseCypherBoolean(expression string, args []interface{}, argumentIndex, parameterIndex *int, params map[string]interface{}) (string, error) {
	if parts, split, err := splitTopLevelBoolean(expression, "OR"); err != nil {
		return "", err
	} else if split {
		clauses := make([]string, 0, len(parts))
		for _, part := range parts {
			clause, err := c.parseCypherBoolean(part, args, argumentIndex, parameterIndex, params)
			if err != nil {
				return "", err
			}
			clauses = append(clauses, clause)
		}
		return "(" + strings.Join(clauses, " OR ") + ")", nil
	}
	if parts, split, err := splitTopLevelBoolean(expression, "AND"); err != nil {
		return "", err
	} else if split {
		clauses := make([]string, 0, len(parts))
		for _, part := range parts {
			clause, err := c.parseCypherBoolean(part, args, argumentIndex, parameterIndex, params)
			if err != nil {
				return "", err
			}
			clauses = append(clauses, clause)
		}
		return "(" + strings.Join(clauses, " AND ") + ")", nil
	}
	leaf, err := trimBalancedOuterParentheses(strings.TrimSpace(expression))
	if err != nil {
		return "", err
	}
	return c.parseCypherLeaf(leaf, args, argumentIndex, parameterIndex, params)
}

func (c *Neo4jConnection) parseCypherLeaf(condition string, args []interface{}, argumentIndex, parameterIndex *int, params map[string]interface{}) (string, error) {
	if condition == "1 = 0" {
		return "false", nil
	}
	upper := strings.ToUpper(condition)
	if strings.HasSuffix(upper, " IS NULL") || strings.HasSuffix(upper, " IS NOT NULL") {
		suffix := " IS NULL"
		if strings.HasSuffix(upper, " IS NOT NULL") {
			suffix = " IS NOT NULL"
		}
		field, err := cypherIdentifier(condition[:len(condition)-len(suffix)])
		if err != nil {
			return "", err
		}
		return "n." + field + suffix, nil
	}

	for _, collectionOperator := range []struct {
		keyword string
		negated bool
	}{
		{keyword: " NOT IN ", negated: true},
		{keyword: " IN ", negated: false},
	} {
		if fieldText, tail, ok := splitCypherCondition(condition, collectionOperator.keyword); ok {
			if !cypherCollectionPlaceholders.MatchString(tail) {
				return "", fmt.Errorf("%w: Neo4j 集合条件占位符非法", ErrInvalidQuery)
			}
			field, err := cypherIdentifier(fieldText)
			if err != nil {
				return "", err
			}
			values, err := consumeCypherValues(args, argumentIndex, strings.Count(tail, "?"))
			if err != nil {
				return "", err
			}
			name := nextCypherParameter(parameterIndex)
			params[name] = values
			clause := fmt.Sprintf("n.%s IN $%s", field, name)
			if collectionOperator.negated {
				clause = "NOT (" + clause + ")"
			}
			return clause, nil
		}
	}

	for _, likeOperator := range []struct {
		keyword string
		negated bool
	}{
		{keyword: " NOT LIKE ", negated: true},
		{keyword: " LIKE ", negated: false},
	} {
		if fieldText, tail, ok := splitCypherCondition(condition, likeOperator.keyword); ok {
			if strings.TrimSpace(tail) != "?" {
				return "", fmt.Errorf("%w: Neo4j LIKE 必须使用一个占位符", ErrInvalidQuery)
			}
			field, err := cypherIdentifier(fieldText)
			if err != nil {
				return "", err
			}
			value, err := consumeCypherValue(args, argumentIndex)
			if err != nil {
				return "", err
			}
			pattern, err := cypherLikeRegex(value)
			if err != nil {
				return "", err
			}
			name := nextCypherParameter(parameterIndex)
			params[name] = pattern
			clause := fmt.Sprintf("n.%s =~ $%s", field, name)
			if likeOperator.negated {
				clause = "NOT (" + clause + ")"
			}
			return clause, nil
		}
	}

	if fieldText, tail, ok := splitCypherCondition(condition, " BETWEEN "); ok {
		if strings.ToUpper(strings.Join(strings.Fields(tail), " ")) != "? AND ?" {
			return "", fmt.Errorf("%w: Neo4j BETWEEN 必须使用两个占位符", ErrInvalidQuery)
		}
		field, err := cypherIdentifier(fieldText)
		if err != nil {
			return "", err
		}
		values, err := consumeCypherValues(args, argumentIndex, 2)
		if err != nil {
			return "", err
		}
		name := nextCypherParameter(parameterIndex)
		params[name+"_start"] = values[0]
		params[name+"_end"] = values[1]
		return fmt.Sprintf("n.%s >= $%s_start AND n.%s <= $%s_end", field, name, field, name), nil
	}

	for _, operator := range []string{">=", "<=", "!=", "<>", ">", "<", "="} {
		keyword := " " + operator + " "
		if fieldText, tail, ok := splitCypherCondition(condition, keyword); ok {
			if strings.TrimSpace(tail) != "?" {
				return "", fmt.Errorf("%w: Neo4j 比较条件必须使用一个占位符", ErrInvalidQuery)
			}
			field, err := cypherIdentifier(fieldText)
			if err != nil {
				return "", err
			}
			value, err := consumeCypherValue(args, argumentIndex)
			if err != nil {
				return "", err
			}
			name := nextCypherParameter(parameterIndex)
			params[name] = value
			if operator == "!=" {
				operator = "<>"
			}
			return fmt.Sprintf("n.%s %s $%s", field, operator, name), nil
		}
	}
	return "", fmt.Errorf("%w: Neo4j 无法解析条件 %q", ErrInvalidQuery, condition)
}

func splitCypherCondition(condition, keyword string) (string, string, bool) {
	index := strings.Index(strings.ToUpper(condition), keyword)
	if index < 0 {
		return "", "", false
	}
	return strings.TrimSpace(condition[:index]), strings.TrimSpace(condition[index+len(keyword):]), true
}

func consumeCypherValue(args []interface{}, index *int) (interface{}, error) {
	if index == nil || *index >= len(args) {
		return nil, fmt.Errorf("%w: Neo4j 条件参数不足", ErrInvalidQuery)
	}
	value := args[*index]
	*index++
	return value, nil
}

func consumeCypherValues(args []interface{}, index *int, count int) ([]interface{}, error) {
	if count < 1 || index == nil || *index+count > len(args) {
		return nil, fmt.Errorf("%w: Neo4j 条件参数不足", ErrInvalidQuery)
	}
	values := append([]interface{}(nil), args[*index:*index+count]...)
	*index += count
	return values, nil
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
