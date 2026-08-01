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
	locationMu       sync.RWMutex
	location         *time.Location
	identity         ConnectionID
	identityOnce     sync.Once
}

func (c *MongoConnection) ConnectionID() ConnectionID {
	if c == nil {
		return ""
	}
	c.identityOnce.Do(func() { c.identity = NewConnectionID("mongo") })
	return c.identity
}

var (
	_ Connection                        = (*MongoConnection)(nil)
	_ RowStreamingConnection            = (*MongoConnection)(nil)
	_ LocationAwareConnection           = (*MongoConnection)(nil)
	_ CapabilityProvider                = (*MongoConnection)(nil)
	_ ModelPrimaryKeyMapper             = (*MongoConnection)(nil)
	_ ModelPrimaryKeyGenerationProvider = (*MongoConnection)(nil)
)

func (c *MongoConnection) Capabilities() DriverCapabilities {
	return DriverCapabilities{
		InsertIDKinds:      []InsertIDKind{InsertIDObjectID, InsertIDString, InsertIDDynamic},
		MatchedCountKnown:  true,
		ModifiedCountKnown: true,
	}
}

// SetLocation 设置 MongoDB 结果和原生时间参数使用的应用时区。
func (c *MongoConnection) SetLocation(location *time.Location) {
	if c == nil {
		return
	}
	if location == nil {
		location = time.Local
	}
	c.locationMu.Lock()
	c.location = location
	c.locationMu.Unlock()
}

// Location 返回 MongoDB 连接使用的应用时区。
func (c *MongoConnection) Location() *time.Location {
	if c == nil {
		return time.Local
	}
	c.locationMu.RLock()
	location := c.location
	c.locationMu.RUnlock()
	if location == nil {
		return time.Local
	}
	return location
}

// StoragePrimaryKey keeps ThinkPHP-style Model.ID ergonomics while using
// MongoDB's intrinsic _id unless the model explicitly chooses another key.
func (c *MongoConnection) StoragePrimaryKey(modelPrimaryKey string, explicitlyConfigured bool) string {
	if !explicitlyConfigured && modelPrimaryKey == "id" {
		return "_id"
	}
	return modelPrimaryKey
}

// GeneratesStoragePrimaryKey reflects MongoDB's only implicit key contract:
// the server generates _id, never a separately selected business key.
func (c *MongoConnection) GeneratesStoragePrimaryKey(storagePrimaryKey string) bool {
	return storagePrimaryKey == "_id"
}

func (c *MongoConnection) Select(parent context.Context, request SelectRequest) ([]map[string]interface{}, error) {
	if request.Aggregate() != nil {
		return nil, fmt.Errorf("%w: MongoDB aggregate expressions require the native aggregate pipeline", ErrInvalidQuery)
	}
	return c.selectRequest(parent, request)
}

// SelectEach 按行消费 MongoDB 游标，回调返回 false 时立即停止读取。
func (c *MongoConnection) SelectEach(parent context.Context, request SelectRequest, callback func(map[string]interface{}) bool) error {
	if request.Aggregate() != nil {
		return fmt.Errorf("%w: MongoDB aggregate expressions require the native aggregate pipeline", ErrInvalidQuery)
	}
	if callback == nil {
		return fmt.Errorf("%w: MongoDB 流式查询回调不能为空", ErrInvalidQuery)
	}
	return c.iterateMongoRequest(parent, request, callback)
}

func (c *MongoConnection) Insert(parent context.Context, request InsertRequest) (InsertResult, error) {
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return InsertResult{}, err
	}
	defer cancel()
	collection, err := c.collection(request.Table())
	if err != nil {
		return InsertResult{}, err
	}
	data := request.Data()
	if len(data) == 0 {
		return InsertResult{}, fmt.Errorf("%w: MongoDB insert data cannot be empty", ErrInvalidQuery)
	}
	if err := validateDataKeys(data); err != nil {
		return InsertResult{}, fmt.Errorf("%w: invalid MongoDB field: %w", ErrInvalidQuery, err)
	}
	data, err = remapMongoPrimaryKeyData(data, request.PrimaryKey(), request.ModelPrimaryKey())
	if err != nil {
		return InsertResult{}, err
	}
	for _, primaryKey := range mongoPrimaryKeyPaths(request.PrimaryKey()) {
		if value, exists := data[primaryKey]; exists {
			converted, err := coerceMongoPrimaryKey(value)
			if err != nil {
				return InsertResult{}, err
			}
			data[primaryKey] = converted
		}
	}
	result, err := collection.InsertOne(ctx, data)
	if err != nil {
		return InsertResult{}, err
	}
	operationResult := InsertResult{
		Affected: 1,
		ID:       result.InsertedID,
		IDKnown:  result.InsertedID != nil,
		Data:     data,
	}
	return operationResult, operationResult.Validate()
}

func (c *MongoConnection) Update(parent context.Context, request UpdateRequest) (UpdateResult, error) {
	where, args, err := request.Predicate().compileNonSQL()
	if err != nil {
		return UpdateResult{}, err
	}
	if len(where) == 0 {
		return UpdateResult{}, ErrUnsafeFullTableMutation
	}
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return UpdateResult{}, err
	}
	defer cancel()
	collection, err := c.collection(request.Table())
	if err != nil {
		return UpdateResult{}, err
	}
	data := request.Data()
	if len(data) == 0 {
		return UpdateResult{}, fmt.Errorf("%w: MongoDB update data cannot be empty", ErrInvalidQuery)
	}
	if err := validateDataKeys(data); err != nil {
		return UpdateResult{}, fmt.Errorf("%w: invalid MongoDB field: %w", ErrInvalidQuery, err)
	}
	if err := rejectMongoPrimaryKeyUpdate(data, request.PrimaryKey(), request.ModelPrimaryKey()); err != nil {
		return UpdateResult{}, err
	}
	filter, err := c.buildFilterWithPrimaryKeyAlias(where, args, request.PrimaryKey(), request.ModelPrimaryKey())
	if err != nil {
		return UpdateResult{}, err
	}
	result, err := collection.UpdateMany(ctx, filter, bson.M{"$set": data})
	if err != nil {
		return UpdateResult{}, err
	}
	operationResult := UpdateResult{
		Affected:      result.ModifiedCount,
		Matched:       result.MatchedCount,
		Modified:      result.ModifiedCount,
		MatchedKnown:  true,
		ModifiedKnown: true,
		Data:          data,
	}
	return operationResult, operationResult.Validate()
}

func (c *MongoConnection) Delete(parent context.Context, request DeleteRequest) (DeleteResult, error) {
	where, args, err := request.Predicate().compileNonSQL()
	if err != nil {
		return DeleteResult{}, err
	}
	if len(where) == 0 {
		return DeleteResult{}, ErrUnsafeFullTableMutation
	}
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return DeleteResult{}, err
	}
	defer cancel()
	collection, err := c.collection(request.Table())
	if err != nil {
		return DeleteResult{}, err
	}
	filter, err := c.buildFilterWithPrimaryKeyAlias(where, args, request.PrimaryKey(), request.ModelPrimaryKey())
	if err != nil {
		return DeleteResult{}, err
	}
	result, err := collection.DeleteMany(ctx, filter)
	if err != nil {
		return DeleteResult{}, err
	}
	operationResult := DeleteResult{Deleted: result.DeletedCount}
	return operationResult, operationResult.Validate()
}

func (c *MongoConnection) Count(parent context.Context, request CountRequest) (int64, error) {
	where, args, err := request.Predicate().compileNonSQL()
	if err != nil {
		return 0, err
	}
	return c.countRequest(parent, request.Table(), where, args, request.PrimaryKey(), request.ModelPrimaryKey())
}

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

func (c *MongoConnection) selectRequest(parent context.Context, request SelectRequest) ([]map[string]interface{}, error) {
	capacity := 0
	if request.Limit() > 0 {
		capacity = request.Limit()
	}
	rows := make([]map[string]interface{}, 0, capacity)
	err := c.iterateMongoRequest(parent, request, func(row map[string]interface{}) bool {
		rows = append(rows, row)
		return true
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// iterateMongoRequest 统一管理 MongoDB 游标生命周期，让 Select 和 SelectEach 共享单行解码路径。
func (c *MongoConnection) iterateMongoRequest(parent context.Context, request SelectRequest, callback func(map[string]interface{}) bool) (resultErr error) {
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return err
	}
	defer cancel()
	collection, err := c.collection(request.Table())
	if err != nil {
		return err
	}
	filter, findOptions, err := c.mongoFindParts(request)
	if err != nil {
		return err
	}
	cursor, err := collection.Find(ctx, filter, findOptions)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, closeMongoCursor(ctx, cursor))
	}()

	for cursor.Next(ctx) {
		var document bson.M
		if err := cursor.Decode(&document); err != nil {
			return err
		}
		row := normalizeMongoDocumentInPlaceWithAliasInLocation(document, request.PrimaryKey(), request.ModelPrimaryKey(), c.Location())
		if mongoProjectionExcludesID(request.Fields()) {
			delete(row, "_id")
		}
		if !callback(row) {
			return nil
		}
	}
	return cursor.Err()
}

func (c *MongoConnection) mongoFindParts(request SelectRequest) (bson.M, *options.FindOptionsBuilder, error) {
	if request.Limit() < 0 || request.Offset() < 0 {
		return nil, nil, ErrInvalidPagination
	}
	clauses, args, err := request.Predicate().compileNonSQL()
	if err != nil {
		return nil, nil, err
	}
	filter, err := c.buildFilterWithPrimaryKeyAlias(clauses, args, request.PrimaryKey(), request.ModelPrimaryKey())
	if err != nil {
		return nil, nil, err
	}

	findOptions := options.Find()
	fields := request.Fields()
	if fields != "" && fields != "*" {
		projection := bson.M{}
		selectedID := false
		for _, rawField := range strings.Split(fields, ",") {
			field := strings.TrimSpace(rawField)
			if err := validateIdentifier(field); err != nil {
				return nil, nil, fmt.Errorf("%w: 非法 MongoDB 投影字段: %w", ErrInvalidQuery, err)
			}
			storageField := mongoStoragePrimaryKeyField(field, request.PrimaryKey(), request.ModelPrimaryKey())
			projection[storageField] = 1
			selectedID = selectedID || storageField == "_id"
		}
		if !selectedID {
			projection["_id"] = 0
		}
		findOptions.SetProjection(projection)
	}
	if order := request.Order(); order != "" {
		if err := validateOrderClause(order); err != nil {
			return nil, nil, fmt.Errorf("%w: 非法 MongoDB 排序: %w", ErrInvalidQuery, err)
		}
		sortFields := bson.D{}
		for _, item := range strings.Split(order, ",") {
			parts := strings.Fields(item)
			direction := 1
			if len(parts) == 2 && strings.EqualFold(parts[1], "desc") {
				direction = -1
			}
			field := mongoStoragePrimaryKeyField(parts[0], request.PrimaryKey(), request.ModelPrimaryKey())
			sortFields = append(sortFields, bson.E{Key: field, Value: direction})
		}
		findOptions.SetSort(sortFields)
	}
	if request.Limit() > 0 {
		findOptions.SetLimit(int64(request.Limit()))
	}
	if request.Offset() > 0 {
		findOptions.SetSkip(int64(request.Offset()))
	}
	return filter, findOptions, nil
}

func mongoProjectionExcludesID(fields string) bool {
	if fields == "" || fields == "*" {
		return false
	}
	for _, rawField := range strings.Split(fields, ",") {
		if strings.TrimSpace(rawField) == "_id" {
			return false
		}
	}
	return true
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

func (c *MongoConnection) countRequest(parent context.Context, table string, clauses []string, args []interface{}, primaryKey string, modelPrimaryKey string) (int64, error) {
	ctx, cancel, err := c.operationContext(parent)
	if err != nil {
		return 0, err
	}
	defer cancel()
	collection, err := c.collection(table)
	if err != nil {
		return 0, err
	}
	filter, err := c.buildFilterWithPrimaryKeyAlias(clauses, args, primaryKey, modelPrimaryKey)
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
	return c.buildFilterWithPrimaryKey(where, args, "")
}

func (c *MongoConnection) buildFilterWithPrimaryKey(where []string, args []interface{}, primaryKey string) (bson.M, error) {
	return c.buildFilterWithPrimaryKeyAlias(where, args, primaryKey, primaryKey)
}

func (c *MongoConnection) buildFilterWithPrimaryKeyAlias(where []string, args []interface{}, primaryKey string, modelPrimaryKey string) (bson.M, error) {
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
	filter := combineMongoAnd(filters)
	if err := coerceMongoFilterPrimaryKeyAlias(filter, primaryKey, modelPrimaryKey); err != nil {
		return nil, err
	}
	return filter, nil
}

func coerceMongoFilterPrimaryKeyAlias(filter bson.M, primaryKey string, modelPrimaryKey string) error {
	for key, value := range filter {
		if primaryKey != modelPrimaryKey && key == modelPrimaryKey {
			if _, exists := filter[primaryKey]; exists {
				return fmt.Errorf("%w: MongoDB primary key filter contains both %q and %q", ErrInvalidQuery, modelPrimaryKey, primaryKey)
			}
			converted, err := coerceMongoPrimaryCondition(value)
			if err != nil {
				return err
			}
			delete(filter, key)
			filter[primaryKey] = converted
			continue
		}
		if isMongoPrimaryKeyPath(key, primaryKey) {
			converted, err := coerceMongoPrimaryCondition(value)
			if err != nil {
				return err
			}
			filter[key] = converted
			continue
		}
		switch key {
		case "$and", "$or", "$nor":
			if err := coerceMongoLogicalFilters(value, primaryKey, modelPrimaryKey); err != nil {
				return err
			}
		}
	}
	return nil
}

func mongoPrimaryKeyPaths(primaryKey string) []string {
	if primaryKey == "" || primaryKey == "_id" {
		return []string{"_id"}
	}
	return []string{"_id", primaryKey}
}

func mongoStoragePrimaryKeyField(field string, primaryKey string, modelPrimaryKey string) string {
	if primaryKey != "" && modelPrimaryKey != "" && primaryKey != modelPrimaryKey && field == modelPrimaryKey {
		return primaryKey
	}
	return field
}

func remapMongoPrimaryKeyData(data map[string]interface{}, primaryKey string, modelPrimaryKey string) (map[string]interface{}, error) {
	if primaryKey == "" || modelPrimaryKey == "" || primaryKey == modelPrimaryKey {
		return data, nil
	}
	value, exists := data[modelPrimaryKey]
	if !exists {
		return data, nil
	}
	if _, collision := data[primaryKey]; collision {
		return nil, fmt.Errorf("%w: MongoDB insert contains both model key %q and storage key %q", ErrInvalidQuery, modelPrimaryKey, primaryKey)
	}
	delete(data, modelPrimaryKey)
	data[primaryKey] = value
	return data, nil
}

func rejectMongoPrimaryKeyUpdate(data map[string]interface{}, primaryKey string, modelPrimaryKey string) error {
	if primaryKey != "_id" {
		return nil
	}
	if _, exists := data[primaryKey]; exists {
		return fmt.Errorf("%w: MongoDB intrinsic primary key %q cannot be updated", ErrInvalidQuery, primaryKey)
	}
	if modelPrimaryKey != "" && modelPrimaryKey != primaryKey {
		if _, exists := data[modelPrimaryKey]; exists {
			return fmt.Errorf("%w: MongoDB intrinsic primary key alias %q cannot be updated", ErrInvalidQuery, modelPrimaryKey)
		}
	}
	return nil
}

func isMongoPrimaryKeyPath(field string, primaryKey string) bool {
	return primaryKey != "" && (field == "_id" || field == primaryKey)
}

func coerceMongoLogicalFilters(value interface{}, primaryKey string, modelPrimaryKey string) error {
	switch typed := value.(type) {
	case []bson.M:
		for _, filter := range typed {
			if err := coerceMongoFilterPrimaryKeyAlias(filter, primaryKey, modelPrimaryKey); err != nil {
				return err
			}
		}
		return nil
	case bson.A:
		for _, item := range typed {
			filter, ok := item.(bson.M)
			if !ok {
				return fmt.Errorf("%w: MongoDB 逻辑主键条件结构非法", ErrInvalidQuery)
			}
			if err := coerceMongoFilterPrimaryKeyAlias(filter, primaryKey, modelPrimaryKey); err != nil {
				return err
			}
		}
		return nil
	case []interface{}:
		for _, item := range typed {
			filter, ok := item.(bson.M)
			if !ok {
				return fmt.Errorf("%w: MongoDB 逻辑主键条件结构非法", ErrInvalidQuery)
			}
			if err := coerceMongoFilterPrimaryKeyAlias(filter, primaryKey, modelPrimaryKey); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: MongoDB 逻辑主键条件结构非法", ErrInvalidQuery)
	}
}

func coerceMongoPrimaryCondition(value interface{}) (interface{}, error) {
	operators, ok := value.(bson.M)
	if !ok {
		return coerceMongoPrimaryKey(value)
	}
	for operator, operand := range operators {
		switch operator {
		case "$eq", "$ne", "$gt", "$gte", "$lt", "$lte":
			converted, err := coerceMongoPrimaryKey(operand)
			if err != nil {
				return nil, err
			}
			operators[operator] = converted
		case "$in", "$nin":
			converted, err := coerceMongoPrimaryList(operand)
			if err != nil {
				return nil, err
			}
			operators[operator] = converted
		case "$not":
			converted, err := coerceMongoPrimaryCondition(operand)
			if err != nil {
				return nil, err
			}
			operators[operator] = converted
		}
	}
	return operators, nil
}

func coerceMongoPrimaryList(value interface{}) (interface{}, error) {
	switch typed := value.(type) {
	case bson.A:
		converted := make(bson.A, len(typed))
		for index, item := range typed {
			value, err := coerceMongoPrimaryKey(item)
			if err != nil {
				return nil, err
			}
			converted[index] = value
		}
		return converted, nil
	case []interface{}:
		converted := make([]interface{}, len(typed))
		for index, item := range typed {
			value, err := coerceMongoPrimaryKey(item)
			if err != nil {
				return nil, err
			}
			converted[index] = value
		}
		return converted, nil
	default:
		return nil, fmt.Errorf("%w: MongoDB 主键集合条件结构非法", ErrInvalidQuery)
	}
}

func coerceMongoPrimaryKey(value interface{}) (interface{}, error) {
	if value == nil {
		return nil, nil
	}
	for {
		reflected := reflect.ValueOf(value)
		if reflected.Kind() != reflect.Pointer && reflected.Kind() != reflect.Interface {
			break
		}
		if reflected.IsNil() {
			return nil, nil
		}
		value = reflected.Elem().Interface()
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.String {
		return value, nil
	}
	text := reflected.String()
	objectID, err := bson.ObjectIDFromHex(text)
	if err != nil {
		return nil, fmt.Errorf("%w: MongoDB 主键不是合法 ObjectID", ErrInvalidQuery)
	}
	return objectID, nil
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
