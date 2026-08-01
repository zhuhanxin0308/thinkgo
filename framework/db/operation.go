package db

import (
	"fmt"
	"strings"
)

// InsertIDKind 描述驱动可返回的主键类型。
type InsertIDKind string

const (
	InsertIDNone     InsertIDKind = "none"
	InsertIDInteger  InsertIDKind = "integer"
	InsertIDString   InsertIDKind = "string"
	InsertIDObjectID InsertIDKind = "object_id"
	InsertIDDynamic  InsertIDKind = "dynamic"
)

// InsertResult 保留一次插入的行数、真实主键和最终持久化数据。
type InsertResult struct {
	Affected int64
	ID       interface{}
	IDKnown  bool
	Data     map[string]interface{}
}

func (r InsertResult) Validate() error {
	if r.Affected < 0 || (r.IDKnown && r.ID == nil) {
		return ErrInvalidOperationResult
	}
	return nil
}

func (r InsertResult) InsertedID() (interface{}, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if !r.IDKnown {
		return nil, ErrInsertIDUnavailable
	}
	return r.ID, nil
}

// UpdateResult 显式区分驱动原始影响数、匹配数和真实修改数。
type UpdateResult struct {
	Affected      int64
	Matched       int64
	Modified      int64
	MatchedKnown  bool
	ModifiedKnown bool
	Data          map[string]interface{}
}

func (r UpdateResult) Validate() error {
	if r.Affected < 0 || (r.MatchedKnown && r.Matched < 0) || (r.ModifiedKnown && r.Modified < 0) {
		return ErrInvalidOperationResult
	}
	if r.MatchedKnown && r.ModifiedKnown && r.Modified > r.Matched {
		return ErrInvalidOperationResult
	}
	return nil
}

// Count 返回 ThinkPHP 风格的简化更新数量：可得时优先使用真实修改数。
func (r UpdateResult) Count() int64 {
	if r.ModifiedKnown {
		return r.Modified
	}
	return r.Affected
}

// DeleteResult 保留节点/记录删除数以及可能存在的关联删除数。
type DeleteResult struct {
	Deleted             int64
	RelatedDeleted      int64
	RelatedDeletedKnown bool
}

func (r DeleteResult) Validate() error {
	if r.Deleted < 0 || (r.RelatedDeletedKnown && r.RelatedDeleted < 0) {
		return ErrInvalidOperationResult
	}
	return nil
}

// DriverCapabilities 描述 Connection 可证明的结果语义。
type DriverCapabilities struct {
	InsertIDKinds      []InsertIDKind
	MatchedCountKnown  bool
	ModifiedCountKnown bool
}

type CapabilityProvider interface {
	Capabilities() DriverCapabilities
}

// ModelPrimaryKeyMapper lets a driver distinguish the field exposed by a
// model from the physical key used by the backend. SQL drivers normally keep
// both names identical; MongoDB maps the default model field id to _id.
type ModelPrimaryKeyMapper interface {
	StoragePrimaryKey(modelPrimaryKey string, explicitlyConfigured bool) string
}

// ModelPrimaryKeyGenerationProvider reports whether a backend generates a
// value for a physical primary key when that key is omitted from an insert.
// It prevents a generated backend identifier from being mistaken for an
// explicitly selected business key.
type ModelPrimaryKeyGenerationProvider interface {
	GeneratesStoragePrimaryKey(storagePrimaryKey string) bool
}

// AggregateExpression 仅表达框架白名单内的单字段聚合。
type AggregateExpression struct {
	Function string
	Field    string
	Alias    string
}

func (expression AggregateExpression) validate() error {
	switch strings.ToUpper(strings.TrimSpace(expression.Function)) {
	case "SUM", "AVG", "MIN", "MAX":
	default:
		return fmt.Errorf("%w: 不支持聚合函数 %q", ErrInvalidQuery, expression.Function)
	}
	if err := validateIdentifier(expression.Field); err != nil {
		return fmt.Errorf("%w: 非法聚合字段: %w", ErrInvalidQuery, err)
	}
	if err := validateIdentifier(expression.Alias); err != nil {
		return fmt.Errorf("%w: 非法聚合别名: %w", ErrInvalidQuery, err)
	}
	return nil
}

type SelectRequest struct {
	table      string
	fields     string
	predicate  Predicate
	primaryKey string
	modelKey   string
	order      string
	limit      int
	offset     int
	aggregate  *AggregateExpression
}

func newSelectRequest(table, fields string, predicate Predicate, primaryKey, order string, limit, offset int, aggregate *AggregateExpression, modelKey ...string) SelectRequest {
	var aggregateCopy *AggregateExpression
	if aggregate != nil {
		copied := *aggregate
		aggregateCopy = &copied
	}
	return SelectRequest{table: table, fields: fields, predicate: Predicate{clauses: clonePredicateClauses(predicate.clauses)}, primaryKey: primaryKey, modelKey: resolveRequestModelKey(primaryKey, modelKey), order: order, limit: limit, offset: offset, aggregate: aggregateCopy}
}

func (r SelectRequest) Table() string  { return r.table }
func (r SelectRequest) Fields() string { return r.fields }
func (r SelectRequest) Predicate() Predicate {
	return Predicate{clauses: clonePredicateClauses(r.predicate.clauses)}
}
func (r SelectRequest) PrimaryKey() string { return r.primaryKey }
func (r SelectRequest) ModelPrimaryKey() string {
	return resolvedRequestModelKey(r.primaryKey, r.modelKey)
}
func (r SelectRequest) Order() string { return r.order }
func (r SelectRequest) Limit() int    { return r.limit }
func (r SelectRequest) Offset() int   { return r.offset }
func (r SelectRequest) Aggregate() *AggregateExpression {
	if r.aggregate == nil {
		return nil
	}
	cloned := *r.aggregate
	return &cloned
}

type InsertRequest struct {
	table      string
	data       map[string]interface{}
	primaryKey string
	modelKey   string
	wantID     bool
}

func newInsertRequest(table string, data map[string]interface{}, primaryKey string, wantID bool, modelKey ...string) InsertRequest {
	return InsertRequest{table: table, data: cloneDatabaseMap(data), primaryKey: primaryKey, modelKey: resolveRequestModelKey(primaryKey, modelKey), wantID: wantID}
}

func (r InsertRequest) Table() string                { return r.table }
func (r InsertRequest) Data() map[string]interface{} { return cloneDatabaseMap(r.data) }
func (r InsertRequest) PrimaryKey() string           { return r.primaryKey }
func (r InsertRequest) ModelPrimaryKey() string {
	return resolvedRequestModelKey(r.primaryKey, r.modelKey)
}
func (r InsertRequest) WantsID() bool { return r.wantID }

type UpdateRequest struct {
	table      string
	data       map[string]interface{}
	predicate  Predicate
	primaryKey string
	modelKey   string
}

func newUpdateRequest(table string, data map[string]interface{}, predicate Predicate, primaryKey string, modelKey ...string) UpdateRequest {
	return UpdateRequest{table: table, data: cloneDatabaseMap(data), predicate: Predicate{clauses: clonePredicateClauses(predicate.clauses)}, primaryKey: primaryKey, modelKey: resolveRequestModelKey(primaryKey, modelKey)}
}

func (r UpdateRequest) Table() string                { return r.table }
func (r UpdateRequest) Data() map[string]interface{} { return cloneDatabaseMap(r.data) }
func (r UpdateRequest) Predicate() Predicate {
	return Predicate{clauses: clonePredicateClauses(r.predicate.clauses)}
}
func (r UpdateRequest) PrimaryKey() string { return r.primaryKey }
func (r UpdateRequest) ModelPrimaryKey() string {
	return resolvedRequestModelKey(r.primaryKey, r.modelKey)
}

type DeleteRequest struct {
	table           string
	predicate       Predicate
	primaryKey      string
	modelKey        string
	detachRelations bool
}

func newDeleteRequest(table string, predicate Predicate, primaryKey string, detachRelations bool, modelKey ...string) DeleteRequest {
	return DeleteRequest{table: table, predicate: Predicate{clauses: clonePredicateClauses(predicate.clauses)}, primaryKey: primaryKey, modelKey: resolveRequestModelKey(primaryKey, modelKey), detachRelations: detachRelations}
}

func (r DeleteRequest) Table() string { return r.table }
func (r DeleteRequest) Predicate() Predicate {
	return Predicate{clauses: clonePredicateClauses(r.predicate.clauses)}
}
func (r DeleteRequest) PrimaryKey() string { return r.primaryKey }
func (r DeleteRequest) ModelPrimaryKey() string {
	return resolvedRequestModelKey(r.primaryKey, r.modelKey)
}
func (r DeleteRequest) DetachRelations() bool { return r.detachRelations }

type CountRequest struct {
	table      string
	predicate  Predicate
	primaryKey string
	modelKey   string
}

func newCountRequest(table string, predicate Predicate, primaryKey string, modelKey ...string) CountRequest {
	return CountRequest{table: table, predicate: Predicate{clauses: clonePredicateClauses(predicate.clauses)}, primaryKey: primaryKey, modelKey: resolveRequestModelKey(primaryKey, modelKey)}
}

func (r CountRequest) Table() string { return r.table }
func (r CountRequest) Predicate() Predicate {
	return Predicate{clauses: clonePredicateClauses(r.predicate.clauses)}
}
func (r CountRequest) PrimaryKey() string { return r.primaryKey }
func (r CountRequest) ModelPrimaryKey() string {
	return resolvedRequestModelKey(r.primaryKey, r.modelKey)
}

func resolveRequestModelKey(primaryKey string, modelKey []string) string {
	if len(modelKey) > 0 && modelKey[0] != "" {
		return modelKey[0]
	}
	return primaryKey
}

func resolvedRequestModelKey(primaryKey string, modelKey string) string {
	if modelKey != "" {
		return modelKey
	}
	return primaryKey
}
