package db

// ValidateDriverIdentifier 供独立驱动复用框架的标识符安全规则。
func ValidateDriverIdentifier(name string) error { return validateIdentifier(name) }

// ValidateDriverData 校验驱动写入数据的字段名，不改变字段值。
func ValidateDriverData(data map[string]interface{}) error { return validateDataKeys(data) }

// ValidateDriverOrder 校验可移植的字段排序规则。
func ValidateDriverOrder(order string) error { return validateOrderClause(order) }

// CloneDriverData 供驱动在重试和异步执行前隔离可变输入。
func CloneDriverData(data map[string]interface{}) map[string]interface{} {
	return cloneDatabaseMap(data)
}

// ParsePredicate 在适配器的参数化文本输入边界创建条件树，校验字段、表达式和占位符数量。
// 显式 SQL 表达式保留其节点类型，可选后端可以拒绝不支持的表达式。
func ParsePredicate(clauses []string, args []interface{}) (Predicate, error) {
	return parsePredicateClauses(clauses, args)
}

// NewSelectRequest 创建独立的查询请求快照，具体后端在执行时校验自身能力与投影规则。
func NewSelectRequest(table, fields string, predicate Predicate, primaryKey, order string, limit, offset int, aggregate *AggregateExpression, modelKey ...string) SelectRequest {
	return newSelectRequest(table, fields, predicate, primaryKey, order, limit, offset, aggregate, modelKey...)
}

// NewInsertRequest 创建独立的插入请求快照。
func NewInsertRequest(table string, data map[string]interface{}, primaryKey string, wantID bool, modelKey ...string) InsertRequest {
	return newInsertRequest(table, data, primaryKey, wantID, modelKey...)
}

// NewUpdateRequest 创建独立的更新请求快照。
func NewUpdateRequest(table string, data map[string]interface{}, predicate Predicate, primaryKey string, modelKey ...string) UpdateRequest {
	return newUpdateRequest(table, data, predicate, primaryKey, modelKey...)
}

// NewDeleteRequest 创建独立的删除请求快照。
func NewDeleteRequest(table string, predicate Predicate, primaryKey string, detachRelations bool, modelKey ...string) DeleteRequest {
	return newDeleteRequest(table, predicate, primaryKey, detachRelations, modelKey...)
}

// NewCountRequest 创建独立的计数请求快照。
func NewCountRequest(table string, predicate Predicate, primaryKey string, modelKey ...string) CountRequest {
	return newCountRequest(table, predicate, primaryKey, modelKey...)
}

// Predicate 返回当前查询的只读条件树，便于驱动扩展与查询诊断。
func (q *Query) Predicate() (Predicate, error) {
	if err := q.ensureValid(); err != nil {
		return Predicate{}, err
	}
	return q.operationPredicate()
}
