package db

import "fmt"

// maxQueryArguments 是无具体 SQL 方言时的统一绑定参数上限。
// 该值与 PostgreSQL/MySQL 的保守预算一致，用于阻止异常大的查询构建分配。
const (
	maxQueryArguments = 65535
	// maxQueryResultRows 限制单次物化查询的显式分页大小，超大结果应使用 Each 或 Chunk。
	maxQueryResultRows = 100000
)

// validateArgumentBudget 校验追加参数后是否仍处于预算内。
func validateArgumentBudget(current, additional, limit int) error {
	if current < 0 || additional < 0 || limit <= 0 || current > limit || additional > limit-current {
		total := current
		if additional > 0 && total <= int(^uint(0)>>1)-additional {
			total += additional
		}
		return fmt.Errorf("%w: 当前 %d 个，追加 %d 个，预算 %d 个", ErrQueryArgumentsTooMany, total, additional, limit)
	}
	return nil
}

// maxQueryArgumentsForQuery 返回当前查询可用的参数预算。
// 有 SQL 方言时优先使用其绑定参数上限；自定义连接或内存连接使用统一回退上限。
func maxQueryArgumentsForQuery(q *Query) int {
	if q != nil {
		return maxQueryArgumentsForBuilder(q.builder())
	}
	return maxQueryArguments
}

// maxQueryArgumentsForBuilder 返回指定 SQL 方言的参数预算。
func maxQueryArgumentsForBuilder(queryBuilder Builder) int {
	if !isNilDatabaseDependency(queryBuilder) {
		if limit := queryBuilder.MaxBindParams(); limit > 0 && limit < maxQueryArguments {
			return limit
		}
	}
	return maxQueryArguments
}

// databaseQueryBuilder 返回 DB 当前 SQL 连接的方言构建器。
func databaseQueryBuilder(db *DB) Builder {
	if db == nil {
		return nil
	}
	db.mu.RLock()
	managed := db.connection
	db.mu.RUnlock()
	if managed == nil {
		return nil
	}
	sqlConnection, ok := managed.connection.(*SQLConnection)
	if !ok || sqlConnection == nil {
		return nil
	}
	return sqlConnection.Builder
}

// validateBindParameterBudget 校验一条 SQL 的全部绑定参数数量。
func validateBindParameterBudget(queryBuilder Builder, counts ...int) error {
	total := 0
	for _, count := range counts {
		if count < 0 || count > int(^uint(0)>>1)-total {
			return fmt.Errorf("%w: 绑定参数数量溢出", ErrQueryArgumentsTooMany)
		}
		total += count
	}
	if err := validateArgumentBudget(0, total, maxQueryArgumentsForBuilder(queryBuilder)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}
	return nil
}

// validateMaterializedRows 在结果返回调用方前阻止超大切片继续传播。
func validateMaterializedRows(rows []map[string]interface{}) error {
	if len(rows) > maxQueryResultRows {
		return fmt.Errorf("%w: 结果行数 %d 超过 %d", ErrQueryResultTooMany, len(rows), maxQueryResultRows)
	}
	return nil
}

// validateQueryStatementArguments 校验 Query 即将执行的完整 SQL 参数数量。
func (q *Query) validateQueryStatementArguments(counts ...int) error {
	if q == nil {
		return fmt.Errorf("%w: 查询构建器为空", ErrInvalidQuery)
	}
	return validateBindParameterBudget(q.builder(), counts...)
}

// validateQueryArgumentAppend 校验 WHERE 与 HAVING 追加参数的总量。
func (q *Query) validateQueryArgumentAppend(additional int) error {
	if q == nil {
		return fmt.Errorf("%w: 查询构建器为空", ErrInvalidQuery)
	}
	limit := maxQueryArgumentsForQuery(q)
	if len(q.havingArgs) > int(^uint(0)>>1)-len(q.args) {
		return fmt.Errorf("%w: 查询参数数量溢出", ErrQueryArgumentsTooMany)
	}
	return validateArgumentBudget(len(q.args)+len(q.havingArgs), additional, limit)
}

// appendQueryArguments 在写入查询参数前执行统一预算检查。
func (q *Query) appendQueryArguments(target *[]interface{}, values []interface{}) bool {
	if err := q.validateQueryArgumentAppend(len(values)); err != nil {
		q.setError(err)
		return false
	}
	*target = append(*target, values...)
	return true
}

// appendQueryArgument 在追加单个查询参数时避免创建临时切片。
func (q *Query) appendQueryArgument(target *[]interface{}, value interface{}) bool {
	if err := q.validateQueryArgumentAppend(1); err != nil {
		q.setError(err)
		return false
	}
	*target = append(*target, value)
	return true
}

// appendTwoQueryArguments 在追加两个查询参数时避免创建临时切片。
func (q *Query) appendTwoQueryArguments(target *[]interface{}, first, second interface{}) bool {
	if err := q.validateQueryArgumentAppend(2); err != nil {
		q.setError(err)
		return false
	}
	*target = append(*target, first, second)
	return true
}

// replaceHavingArguments 替换 HAVING 参数并按替换后的总量重新校验预算。
func (q *Query) replaceHavingArguments(values []interface{}) bool {
	if q == nil {
		q.setError(fmt.Errorf("%w: 查询构建器为空", ErrInvalidQuery))
		return false
	}
	if err := validateArgumentBudget(len(q.args), len(values), maxQueryArgumentsForQuery(q)); err != nil {
		q.setError(err)
		return false
	}
	q.havingArgs = append(q.havingArgs[:0], values...)
	return true
}
