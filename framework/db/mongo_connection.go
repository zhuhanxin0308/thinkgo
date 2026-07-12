package db

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	defaultMongoOpTimeout = 10 * time.Second
	maxMongoLikeLength    = 4096
)

var mongoCollectionPlaceholders = regexp.MustCompile(`^\(\s*\?(?:\s*,\s*\?)*\s*\)$`)

// MongoConnection 将统一查询接口映射为有界上下文的 MongoDB 操作。
type MongoConnection struct {
	Client           *mongo.Client
	Database         string
	OperationTimeout time.Duration
	closeOnce        sync.Once
	closeErr         error
}

var _ ContextualConnection = (*MongoConnection)(nil)

func requireMongoScalar(value interface{}) error {
	if value == nil {
		return nil
	}
	for {
		reflected := reflect.ValueOf(value)
		if reflected.Kind() != reflect.Pointer && reflected.Kind() != reflect.Interface {
			break
		}
		if reflected.IsNil() {
			return nil
		}
		value = reflected.Elem().Interface()
	}
	switch value.(type) {
	case time.Time, bson.ObjectID, bson.DateTime, bson.Decimal128, bson.Binary:
		return nil
	}
	switch reflect.TypeOf(value).Kind() {
	case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct:
		if _, ok := value.([]byte); ok {
			return nil
		}
		return fmt.Errorf("%w: MongoDB 条件值必须为标量，实际为 %T", ErrInvalidQuery, value)
	default:
		return nil
	}
}

func (c *MongoConnection) validate() error {
	if c == nil || c.Client == nil || strings.TrimSpace(c.Database) == "" {
		return ErrDatabaseUnavailable
	}
	return nil
}

func (c *MongoConnection) operationContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	if err := c.validate(); err != nil {
		return nil, nil, err
	}
	if parent == nil {
		return nil, nil, fmt.Errorf("%w: MongoDB 上下文不能为空", ErrInvalidQuery)
	}
	timeout := c.OperationTimeout
	if timeout <= 0 {
		timeout = defaultMongoOpTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	return ctx, cancel, nil
}

func (c *MongoConnection) collection(name string) (*mongo.Collection, error) {
	if err := validateIdentifier(name); err != nil {
		return nil, fmt.Errorf("%w: 非法 MongoDB 集合名: %w", ErrInvalidQuery, err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c.Client.Database(c.Database).Collection(name), nil
}

func (c *MongoConnection) Select(table, fields string, where []string, args []interface{}, order string, limit, offset int) ([]map[string]interface{}, error) {
	return c.SelectContext(context.Background(), table, fields, where, args, order, limit, offset)
}

func (c *MongoConnection) SelectContext(parent context.Context, table, fields string, where []string, args []interface{}, order string, limit, offset int) ([]map[string]interface{}, error) {
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return nil, err
	}
	defer cancel()
	collection, err := c.collection(table)
	if err != nil {
		return nil, err
	}
	if limit < 0 || offset < 0 {
		return nil, ErrInvalidPagination
	}
	filter, err := c.buildFilter(where, args)
	if err != nil {
		return nil, err
	}
	findOptions := options.Find()
	if fields != "" && fields != "*" {
		projection := bson.M{}
		for _, rawField := range strings.Split(fields, ",") {
			field := strings.TrimSpace(rawField)
			if err := validateIdentifier(field); err != nil {
				return nil, fmt.Errorf("%w: 非法 MongoDB 投影字段: %w", ErrInvalidQuery, err)
			}
			projection[field] = 1
		}
		findOptions.SetProjection(projection)
	}
	if order != "" {
		if err := validateOrderClause(order); err != nil {
			return nil, fmt.Errorf("%w: 非法 MongoDB 排序: %w", ErrInvalidQuery, err)
		}
		sortFields := bson.D{}
		for _, item := range strings.Split(order, ",") {
			parts := strings.Fields(item)
			direction := 1
			if len(parts) == 2 && strings.EqualFold(parts[1], "desc") {
				direction = -1
			}
			sortFields = append(sortFields, bson.E{Key: parts[0], Value: direction})
		}
		findOptions.SetSort(sortFields)
	}
	if limit > 0 {
		findOptions.SetLimit(int64(limit))
	}
	if offset > 0 {
		findOptions.SetSkip(int64(offset))
	}

	cursor, err := collection.Find(ctx, filter, findOptions)
	if err != nil {
		return nil, err
	}
	var documents []bson.M
	readErr := cursor.All(ctx, &documents)
	closeErr := closeMongoCursor(ctx, cursor)
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	rows := make([]map[string]interface{}, len(documents))
	for index, document := range documents {
		rows[index] = normalizeMongoDocument(document)
	}
	return rows, nil
}

// closeMongoCursor 使用独立截止时间清理服务端游标，避免业务取消信号阻断资源释放。
func closeMongoCursor(parent context.Context, cursor *mongo.Cursor) error {
	base := context.Background()
	if parent != nil {
		base = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithTimeout(base, defaultMongoOpTimeout)
	defer cancel()
	return cursor.Close(ctx)
}

func (c *MongoConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	return c.InsertContext(context.Background(), table, data)
}

func (c *MongoConnection) InsertContext(parent context.Context, table string, data map[string]interface{}) (int64, error) {
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return 0, err
	}
	defer cancel()
	collection, err := c.collection(table)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, fmt.Errorf("%w: MongoDB 插入数据不能为空", ErrInvalidQuery)
	}
	if err := validateDataKeys(data); err != nil {
		return 0, fmt.Errorf("%w: 非法 MongoDB 字段: %w", ErrInvalidQuery, err)
	}
	if _, err := collection.InsertOne(ctx, cloneDatabaseMap(data)); err != nil {
		return 0, err
	}
	return 1, nil
}

func (c *MongoConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	return c.UpdateContext(context.Background(), table, data, where, args)
}

func (c *MongoConnection) UpdateContext(parent context.Context, table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	if len(where) == 0 {
		return 0, ErrUnsafeFullTableMutation
	}
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return 0, err
	}
	defer cancel()
	collection, err := c.collection(table)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, fmt.Errorf("%w: MongoDB 更新数据不能为空", ErrInvalidQuery)
	}
	if err := validateDataKeys(data); err != nil {
		return 0, fmt.Errorf("%w: 非法 MongoDB 字段: %w", ErrInvalidQuery, err)
	}
	filter, err := c.buildFilter(where, args)
	if err != nil {
		return 0, err
	}
	result, err := collection.UpdateMany(ctx, filter, bson.M{"$set": cloneDatabaseMap(data)})
	if err != nil {
		return 0, err
	}
	return result.ModifiedCount, nil
}

func (c *MongoConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	return c.DeleteContext(context.Background(), table, where, args)
}

func (c *MongoConnection) DeleteContext(parent context.Context, table string, where []string, args []interface{}) (int64, error) {
	if len(where) == 0 {
		return 0, ErrUnsafeFullTableMutation
	}
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return 0, err
	}
	defer cancel()
	collection, err := c.collection(table)
	if err != nil {
		return 0, err
	}
	filter, err := c.buildFilter(where, args)
	if err != nil {
		return 0, err
	}
	result, err := collection.DeleteMany(ctx, filter)
	if err != nil {
		return 0, err
	}
	return result.DeletedCount, nil
}

func (c *MongoConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	return c.CountContext(context.Background(), table, where, args)
}

func (c *MongoConnection) CountContext(parent context.Context, table string, where []string, args []interface{}) (int64, error) {
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return 0, err
	}
	defer cancel()
	collection, err := c.collection(table)
	if err != nil {
		return 0, err
	}
	filter, err := c.buildFilter(where, args)
	if err != nil {
		return 0, err
	}
	return collection.CountDocuments(ctx, filter)
}

func (c *MongoConnection) Close() error {
	if c == nil || c.Client == nil {
		return ErrDatabaseUnavailable
	}
	c.closeOnce.Do(func() {
		timeout := c.OperationTimeout
		if timeout <= 0 {
			timeout = defaultMongoOpTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		c.closeErr = c.Client.Disconnect(ctx)
	})
	return c.closeErr
}

func (c *MongoConnection) buildFilter(where []string, args []interface{}) (bson.M, error) {
	filters := make([]bson.M, 0, len(where))
	argumentIndex := 0
	for _, expression := range where {
		filter, err := c.parseMongoBoolean(expression, args, &argumentIndex)
		if err != nil {
			return nil, err
		}
		filters = append(filters, filter)
	}
	if argumentIndex != len(args) {
		return nil, fmt.Errorf("%w: MongoDB 条件使用 %d 个参数，实际传入 %d 个", ErrInvalidQuery, argumentIndex, len(args))
	}
	return combineMongoAnd(filters), nil
}

func (c *MongoConnection) parseMongoBoolean(expression string, args []interface{}, argumentIndex *int) (bson.M, error) {
	if parts, split, err := splitTopLevelBoolean(expression, "OR"); err != nil {
		return nil, err
	} else if split {
		alternatives := make([]bson.M, 0, len(parts))
		for _, part := range parts {
			filter, err := c.parseMongoBoolean(part, args, argumentIndex)
			if err != nil {
				return nil, err
			}
			alternatives = append(alternatives, filter)
		}
		return bson.M{"$or": alternatives}, nil
	}
	if parts, split, err := splitTopLevelBoolean(expression, "AND"); err != nil {
		return nil, err
	} else if split {
		filters := make([]bson.M, 0, len(parts))
		for _, part := range parts {
			filter, err := c.parseMongoBoolean(part, args, argumentIndex)
			if err != nil {
				return nil, err
			}
			filters = append(filters, filter)
		}
		return combineMongoAnd(filters), nil
	}
	leaf, err := trimBalancedOuterParentheses(strings.TrimSpace(expression))
	if err != nil {
		return nil, err
	}
	return c.parseMongoLeaf(leaf, args, argumentIndex)
}

func (c *MongoConnection) parseMongoLeaf(condition string, args []interface{}, argumentIndex *int) (bson.M, error) {
	if condition == "1 = 0" {
		return bson.M{"$expr": bson.M{"$eq": bson.A{1, 0}}}, nil
	}
	upper := strings.ToUpper(condition)
	if strings.HasSuffix(upper, " IS NULL") {
		field, err := mongoField(condition[:len(condition)-8])
		if err != nil {
			return nil, err
		}
		return bson.M{field: nil}, nil
	}
	if strings.HasSuffix(upper, " IS NOT NULL") {
		field, err := mongoField(condition[:len(condition)-12])
		if err != nil {
			return nil, err
		}
		return bson.M{field: bson.M{"$ne": nil}}, nil
	}

	for _, collectionOperator := range []struct {
		keyword string
		mongo   string
	}{
		{keyword: " NOT IN ", mongo: "$nin"},
		{keyword: " IN ", mongo: "$in"},
	} {
		if fieldText, tail, ok := splitMongoCondition(condition, collectionOperator.keyword); ok {
			field, err := mongoField(fieldText)
			if err != nil {
				return nil, err
			}
			if !mongoCollectionPlaceholders.MatchString(tail) {
				return nil, fmt.Errorf("%w: MongoDB 集合条件占位符非法 %q", ErrInvalidQuery, condition)
			}
			count := strings.Count(tail, "?")
			values, err := consumeMongoValues(args, argumentIndex, count)
			if err != nil {
				return nil, err
			}
			return bson.M{field: bson.M{collectionOperator.mongo: values}}, nil
		}
	}

	for _, likeOperator := range []struct {
		keyword string
		negated bool
	}{
		{keyword: " NOT LIKE ", negated: true},
		{keyword: " LIKE ", negated: false},
	} {
		if fieldText, tail, ok := splitMongoCondition(condition, likeOperator.keyword); ok {
			if strings.TrimSpace(tail) != "?" {
				return nil, fmt.Errorf("%w: MongoDB LIKE 条件必须使用一个占位符", ErrInvalidQuery)
			}
			field, err := mongoField(fieldText)
			if err != nil {
				return nil, err
			}
			value, err := consumeMongoScalar(args, argumentIndex)
			if err != nil {
				return nil, err
			}
			regex, err := mongoLikeRegex(value)
			if err != nil {
				return nil, err
			}
			if likeOperator.negated {
				return bson.M{field: bson.M{"$not": regex}}, nil
			}
			return bson.M{field: regex}, nil
		}
	}

	if fieldText, tail, ok := splitMongoCondition(condition, " BETWEEN "); ok {
		if strings.ToUpper(strings.Join(strings.Fields(tail), " ")) != "? AND ?" {
			return nil, fmt.Errorf("%w: MongoDB BETWEEN 必须使用两个占位符", ErrInvalidQuery)
		}
		field, err := mongoField(fieldText)
		if err != nil {
			return nil, err
		}
		values, err := consumeMongoValues(args, argumentIndex, 2)
		if err != nil {
			return nil, err
		}
		return bson.M{field: bson.M{"$gte": values[0], "$lte": values[1]}}, nil
	}

	for _, comparison := range []struct {
		sql   string
		mongo string
	}{
		{sql: ">=", mongo: "$gte"}, {sql: "<=", mongo: "$lte"},
		{sql: "!=", mongo: "$ne"}, {sql: "<>", mongo: "$ne"},
		{sql: ">", mongo: "$gt"}, {sql: "<", mongo: "$lt"}, {sql: "=", mongo: ""},
	} {
		keyword := " " + comparison.sql + " "
		if fieldText, tail, ok := splitMongoCondition(condition, keyword); ok {
			if strings.TrimSpace(tail) != "?" {
				return nil, fmt.Errorf("%w: MongoDB 比较条件必须使用一个占位符", ErrInvalidQuery)
			}
			field, err := mongoField(fieldText)
			if err != nil {
				return nil, err
			}
			value, err := consumeMongoScalar(args, argumentIndex)
			if err != nil {
				return nil, err
			}
			if comparison.mongo == "" {
				return bson.M{field: value}, nil
			}
			return bson.M{field: bson.M{comparison.mongo: value}}, nil
		}
	}
	return nil, fmt.Errorf("%w: MongoDB 无法解析条件 %q", ErrInvalidQuery, condition)
}

func splitMongoCondition(condition, keyword string) (string, string, bool) {
	index := strings.Index(strings.ToUpper(condition), keyword)
	if index < 0 {
		return "", "", false
	}
	return strings.TrimSpace(condition[:index]), strings.TrimSpace(condition[index+len(keyword):]), true
}

func mongoField(field string) (string, error) {
	field = strings.TrimSpace(field)
	if err := validateIdentifier(field); err != nil {
		return "", fmt.Errorf("%w: 非法 MongoDB 字段 %q: %w", ErrInvalidQuery, field, err)
	}
	return field, nil
}

func consumeMongoScalar(args []interface{}, index *int) (interface{}, error) {
	if index == nil || *index >= len(args) {
		return nil, fmt.Errorf("%w: MongoDB 条件参数不足", ErrInvalidQuery)
	}
	value := args[*index]
	if err := requireMongoScalar(value); err != nil {
		return nil, err
	}
	*index++
	return value, nil
}

func consumeMongoValues(args []interface{}, index *int, count int) ([]interface{}, error) {
	if count < 1 || index == nil || *index+count > len(args) {
		return nil, fmt.Errorf("%w: MongoDB 条件参数不足", ErrInvalidQuery)
	}
	values := make([]interface{}, count)
	for offset := 0; offset < count; offset++ {
		value, err := consumeMongoScalar(args, index)
		if err != nil {
			return nil, err
		}
		values[offset] = value
	}
	return values, nil
}

func combineMongoAnd(filters []bson.M) bson.M {
	if len(filters) == 0 {
		return bson.M{}
	}
	result := bson.M{}
	andFilters := make([]bson.M, 0)
	for _, filter := range filters {
		if len(filter) != 1 {
			andFilters = append(andFilters, filter)
			continue
		}
		for key, value := range filter {
			if strings.HasPrefix(key, "$") {
				andFilters = append(andFilters, filter)
				continue
			}
			existing, duplicated := result[key]
			if !duplicated {
				result[key] = value
				continue
			}
			existingOperators, existingOK := existing.(bson.M)
			newOperators, newOK := value.(bson.M)
			if existingOK && newOK && mergeMongoOperators(existingOperators, newOperators) {
				result[key] = existingOperators
				continue
			}
			delete(result, key)
			andFilters = append(andFilters, bson.M{key: existing}, bson.M{key: value})
		}
	}
	if len(andFilters) == 0 {
		return result
	}
	if len(result) > 0 {
		andFilters = append(andFilters, result)
	}
	if len(andFilters) == 1 {
		return andFilters[0]
	}
	return bson.M{"$and": andFilters}
}

func mergeMongoOperators(target, source bson.M) bool {
	for key := range source {
		if _, duplicated := target[key]; duplicated {
			return false
		}
	}
	for key, value := range source {
		target[key] = value
	}
	return true
}

func mongoLikeRegex(raw interface{}) (bson.M, error) {
	var pattern string
	switch typed := raw.(type) {
	case string:
		pattern = typed
	case []byte:
		pattern = string(typed)
	default:
		return nil, fmt.Errorf("%w: MongoDB LIKE 参数必须是字符串", ErrInvalidQuery)
	}
	if len(pattern) > maxMongoLikeLength {
		return nil, fmt.Errorf("%w: MongoDB LIKE 模式过长", ErrInvalidQuery)
	}
	escaped := regexp.QuoteMeta(pattern)
	escaped = strings.ReplaceAll(escaped, "%", ".*")
	escaped = strings.ReplaceAll(escaped, "_", ".")
	return bson.M{"$regex": "^" + escaped + "$", "$options": "i"}, nil
}

func normalizeMongoDocument(document bson.M) map[string]interface{} {
	result := make(map[string]interface{}, len(document))
	for key, value := range document {
		result[key] = normalizeMongoValue(value)
	}
	return result
}

func normalizeMongoValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case bson.ObjectID:
		return typed.Hex()
	case bson.Binary:
		return bson.Binary{Subtype: typed.Subtype, Data: append([]byte(nil), typed.Data...)}
	case bson.M:
		return normalizeMongoDocument(typed)
	case map[string]interface{}:
		return normalizeMongoDocument(bson.M(typed))
	case bson.D:
		result := make(map[string]interface{}, len(typed))
		for _, element := range typed {
			result[element.Key] = normalizeMongoValue(element.Value)
		}
		return result
	case bson.A:
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			result[index] = normalizeMongoValue(item)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			result[index] = normalizeMongoValue(item)
		}
		return result
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}
