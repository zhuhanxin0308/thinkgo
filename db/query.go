package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Query 表示一次独立的链式查询构建过程。
// 每次调用 DB.Name() 都会返回新的 Query 实例，避免跨请求状态污染。
type Query struct {
	db                  *DB
	table               string
	rawTableName        bool // true 表示 table 已包含前缀，不再自动拼接（对应 ThinkPHP 的 Db::table()）
	where               []string
	args                []interface{}
	predicate           Predicate
	limit               int
	offset              int
	order               string
	fields              string
	joins               []joinClause
	group               string
	having              string
	havingArgs          []interface{}
	distinct            bool
	autoTimestamp       bool
	createTimeField     string
	updateTimeField     string
	timestampValueType  string
	insertPrimaryKey    string          // INSERT RETURNING 使用的主键字段，模型层可覆盖默认 id
	modelPrimaryKey     string          // 模型暴露的逻辑主键；为空时与 insertPrimaryKey 相同
	setExprs            []setExpression // Inc/Dec 的结构化表达式，执行时统一引用字段并绑定步长参数
	lockMode            LockMode        // 类型化悲观锁意图，由当前方言生成合法 SQL 或明确拒绝
	aggregateExpression *AggregateExpression
	txExecutor          *sql.Tx         // 绑定的事务对象（若在事务内执行）
	txOwner             *Tx             // 事务生命周期所有者，用于结束状态与连接租约校验
	ctx                 context.Context // 查询上下文，用于超时/取消（默认 context.Background()）
	err                 error
}

type setExpression struct {
	field    string
	operator string
	amount   int
}

// WithContext 绑定查询上下文，使底层 SQL 执行可随请求超时/取消（需连接支持 context）。
// 对应将 HTTP 请求的 context 传入 ORM，避免慢查询在请求结束后仍占用连接。
func (q *Query) WithContext(ctx context.Context) *Query {
	if q == nil {
		return q
	}
	q = q.clone()
	if ctx == nil {
		return q.setError(fmt.Errorf("%w: 查询上下文不能为空", ErrInvalidQuery))
	}
	q.ctx = ctx
	return q
}

// context 返回查询上下文，未设置时回退到 context.Background()。
func (q *Query) context() context.Context {
	if q.ctx != nil {
		return q.ctx
	}
	return context.Background()
}

// withOperationContext 为一次终端数据库操作创建独立查询副本。
// 调用方必须执行返回的 cancel；已有截止时间保持不变，缺失时补齐框架默认超时。
func (q *Query) withOperationContext() (*Query, context.CancelFunc) {
	if q == nil {
		return q, func() {}
	}
	operationContext, cancel := databaseOperationContext(q.context())
	// 终端操作只替换 ctx，不会修改切片或聚合表达式；使用结构体副本即可
	// 保持链式查询不可变，同时避免每次执行都深拷贝完整查询条件。
	cloned := *q
	cloned.ctx = operationContext
	return &cloned, cancel
}

// now 返回当前查询使用的数据库业务时间，保证日历条件和自动时间戳使用同一时区。
func (q *Query) now() time.Time {
	if q == nil || q.db == nil {
		return time.Now().UTC()
	}
	return q.db.Now()
}

// location 返回当前查询使用的应用时区。
func (q *Query) location() *time.Location {
	if q == nil || q.db == nil {
		return time.UTC
	}
	return q.db.Location()
}

// joinClause 描述一条 JOIN 子句。
type joinClause struct {
	joinType  string
	table     string
	condition string
	rawTable  bool // true 表示 table 已是完整表名，构建时不再追加前缀
}

// newQuery 实例化查询构建器。
func newQuery(db *DB, table string, rawTableName ...bool) *Query {
	isRaw := false
	if len(rawTableName) > 0 {
		isRaw = rawTableName[0]
	}
	return newQueryWithOwner(db, table, isRaw, nil)
}

// newQueryWithOwner 让活动事务继续使用既有租约；数据库关闭后仍拒绝普通查询。
func newQueryWithOwner(db *DB, table string, isRaw bool, owner *Tx) *Query {
	query := &Query{
		db:                 db,
		table:              table,
		rawTableName:       isRaw,
		where:              make([]string, 0),
		args:               make([]interface{}, 0),
		joins:              make([]joinClause, 0),
		havingArgs:         make([]interface{}, 0),
		setExprs:           make([]setExpression, 0),
		fields:             "*",
		createTimeField:    "create_time",
		updateTimeField:    "update_time",
		timestampValueType: TimestampValueTypeUnix,
		insertPrimaryKey:   "id",
	}
	if db == nil {
		query.setError(ErrDatabaseUnavailable)
	} else {
		// 查询创建时复制不可变配置，避免后续链式调用依赖共享可变状态。
		db.mu.RLock()
		query.autoTimestamp = db.autoTimestamp
		query.createTimeField = db.createTimeField
		query.updateTimeField = db.updateTimeField
		query.timestampValueType = db.timestampValueType
		closed := db.closed
		connection := db.connection
		db.mu.RUnlock()
		if closed && owner == nil {
			query.setError(ErrDatabaseClosed)
		} else if owner == nil && isNilDatabaseDependency(connection) {
			query.setError(ErrDatabaseUnavailable)
		}
	}
	if owner != nil {
		if owner.db != db {
			query.setError(ErrInvalidTransaction)
		} else {
			owner.bindQuery(query)
		}
	}

	if err := validateIdentifier(table); err != nil {
		query.setError(fmt.Errorf("unsafe table name: %w", err))
	}

	return query
}

// setError 设置错误状态。
func (q *Query) setError(err error) *Query {
	if q == nil || err == nil {
		return q
	}
	if !errors.Is(err, ErrInvalidQuery) {
		err = fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}
	if q.err == nil {
		q.err = err
	} else if !errors.Is(q.err, err) {
		q.err = errors.Join(q.err, err)
	}
	return q
}

// Where 添加查询条件。
// 支持以下多种格式以兼容 ThinkPHP：
// 1. Where("id = ?", 1) - 原生参数化
// 2. Where("status", 1) - 键值等值 shorthand（自动转为 status = ?）
// 3. Where("status", "<>", 1) - 三元组
// 4. Where(map[string]interface{}{"status": 1}) - map
// 5. Where(func(g *ConditionGroup) { ... }) - 闭包条件组
func (q *Query) Where(condition interface{}, args ...interface{}) *Query {
	q = q.clone()
	node, _, err := compileWhereNode(condition, args)
	if err != nil {
		return q.setError(err)
	}
	return q.appendQueryNode("AND", node, q.builder())
}

func (q *Query) WhereOr(condition interface{}, args ...interface{}) *Query {
	q = q.clone()
	node, _, err := compileWhereNode(condition, args)
	if err != nil {
		return q.setError(err)
	}
	return q.appendQueryNode("OR", node, q.builder())
}

func (q *Query) WhereColumn(left string, op string, right string) *Query {
	q = q.clone()
	clause, err := compileColumnClause(left, op, right)
	if err != nil {
		return q.setError(err)
	}
	node, err := parsePredicateText(clause, nil)
	if err != nil {
		return q.setError(err)
	}
	return q.appendQueryNode("AND", node, q.builder())
}

func (q *Query) WhereExp(field string, op string, expression string, args ...interface{}) *Query {
	q = q.clone()
	clause, err := compileExpressionClause(field, op, expression)
	if err != nil {
		return q.setError(err)
	}
	if err := validatePlaceholderCount(expression, len(args)); err != nil {
		return q.setError(err)
	}
	node, err := parsePredicateText(clause, args)
	if err != nil {
		return q.setError(err)
	}
	return q.appendQueryNode("AND", node, q.builder())
}

func (q *Query) WhereTime(field string, operator string, values ...interface{}) *Query {
	valueType := TimestampValueTypeUnix
	if q != nil && q.timestampValueType != "" {
		valueType = q.timestampValueType
	}
	return q.WhereTimeAs(field, valueType, operator, values...)
}

// WhereTimeAs 按指定存储类型添加时间条件，避免把 Unix、字符串和原生时间混为一谈。
func (q *Query) WhereTimeAs(field string, valueType string, operator string, values ...interface{}) *Query {
	q = q.clone()
	if err := validateIdentifier(field); err != nil {
		return q.setError(fmt.Errorf("unsafe whereTime field: %w", err))
	}
	if strings.TrimSpace(valueType) == "" {
		valueType = q.timestampValueType
	}
	if !isSupportedTimestampValueType(valueType) {
		return q.setError(fmt.Errorf("时间条件存储类型不受支持: %q", valueType))
	}
	valueType = normalizeTimestampValueType(valueType)

	normalizedOperator := strings.ToLower(strings.TrimSpace(operator))
	if start, endExclusive, err := buildTimeWindow(normalizedOperator, q.now()); err == nil {
		if len(values) != 0 {
			return q.setError(fmt.Errorf("whereTime %s 不接受额外参数", operator))
		}
		startValue, err := normalizeTimeValueInStorage(start, q.location(), valueType)
		if err != nil {
			return q.setError(err)
		}
		endValue, err := normalizeTimeValueInStorage(endExclusive, q.location(), valueType)
		if err != nil {
			return q.setError(err)
		}
		return q.appendQueryNode("AND", PredicateNode{Kind: PredicateBoolean, Operator: "AND", Children: []PredicateNode{
			comparisonNode(field, ">=", startValue), comparisonNode(field, "<", endValue),
		}}, q.builder())
	}

	if normalizedOperator == "between" {
		if len(values) != 2 {
			return q.setError(fmt.Errorf("whereTime between requires exactly 2 values"))
		}
		minimum, err := normalizeTimeValueInStorage(values[0], q.location(), valueType)
		if err != nil {
			return q.setError(err)
		}
		maximum, err := normalizeTimeValueInStorage(values[1], q.location(), valueType)
		if err != nil {
			return q.setError(err)
		}
		return q.WhereBetween(field, minimum, maximum)
	}

	if len(values) != 1 {
		return q.setError(fmt.Errorf("whereTime %s requires exactly 1 value", operator))
	}
	value, err := normalizeTimeValueInStorage(values[0], q.location(), valueType)
	if err != nil {
		return q.setError(err)
	}
	return q.WhereField(field, operator, value)
}

// WhereField 添加安全的字段条件，对应 ThinkPHP 的 where('field', 'op', value)。
// 框架内部自动校验字段名和操作符，值使用参数化查询，从 API 层面杠绝 SQL 注入。
// 示例: WhereField("age", ">=", 18)、WhereField("status", "=", 1)
func (q *Query) WhereField(field string, op string, value interface{}) *Query {
	q = q.clone()
	return q.appendWhereField(field, op, value)
}

// appendWhereField 在已经完成复制的查询副本上追加字段条件。
// 该内部方法避免批量条件逐项调用公开链式方法时重复复制查询状态。
func (q *Query) appendWhereField(field string, op string, value interface{}) *Query {
	if err := validateIdentifier(field); err != nil {
		return q.setError(fmt.Errorf("unsafe whereField field: %w", err))
	}
	normalizedOperator, err := normalizeOperator(op)
	if err != nil {
		return q.setError(fmt.Errorf("unsafe whereField operator: %w", err))
	}
	return q.appendWhereFieldWithBuilder(field, normalizedOperator, value, q.builder())
}

// appendWhereFieldWithBuilder 在批量条件构造时复用已解析的方言构建器。
func (q *Query) appendWhereFieldWithBuilder(field string, normalizedOperator string, value interface{}, builder Builder) *Query {
	return q.appendQueryNode("AND", comparisonNode(field, normalizedOperator, value), builder)
}

// WhereMap 添加等值条件集合，对应 ThinkPHP 的 where(['name' => 'john', 'status' => 1])。
// 所有键名自动校验、值自动参数化，完全消除 SQL 注入风险。
// 示例: WhereMap(map[string]interface{}{"name": "john", "status": 1})
func (q *Query) WhereMap(conditions map[string]interface{}) *Query {
	return q.Where(conditions)
}

// WhereFields 添加多个三元组条件，对应 ThinkPHP 的 where([['age','>=',18],['status','=',1]])。
// 每个条件是 [field, operator, value] 格式的切片。
// 示例: WhereFields([][]interface{}{{"age", ">=", 18}, {"status", "=", 1}})
func (q *Query) WhereFields(conditions [][]interface{}) *Query {
	q = q.cloneWithCapacity(len(conditions), len(conditions))
	builder := q.builder()
	for _, cond := range conditions {
		if len(cond) != 3 {
			return q.setError(fmt.Errorf("条件必须为 [field, operator, value] 三元组，当前长度: %d", len(cond)))
		}
		field, ok := cond[0].(string)
		if !ok {
			return q.setError(fmt.Errorf("条件字段名必须为 string 类型"))
		}
		op, ok := cond[1].(string)
		if !ok {
			return q.setError(fmt.Errorf("条件操作符必须为 string 类型"))
		}
		if err := validateIdentifier(field); err != nil {
			return q.setError(fmt.Errorf("unsafe whereField field: %w", err))
		}
		normalizedOperator, err := normalizeOperator(op)
		if err != nil {
			return q.setError(fmt.Errorf("unsafe whereField operator: %w", err))
		}
		q = q.appendWhereFieldWithBuilder(field, normalizedOperator, cond[2], builder)
	}
	return q
}

// WhereIn 添加 IN 条件。
func (q *Query) WhereIn(field string, values []interface{}) *Query {
	q = q.clone()
	if err := validateIdentifier(field); err != nil {
		return q.setError(fmt.Errorf("unsafe whereIn field: %w", err))
	}
	if len(values) == 0 {
		return q.appendQueryNode("AND", PredicateNode{Kind: PredicateFalse}, q.builder())
	}
	return q.appendQueryNode("AND", comparisonNode(field, "IN", values...), q.builder())
}

// WhereNotIn 添加 NOT IN 条件。
func (q *Query) WhereNotIn(field string, values []interface{}) *Query {
	q = q.clone()
	if err := validateIdentifier(field); err != nil {
		return q.setError(fmt.Errorf("unsafe whereNotIn field: %w", err))
	}
	if len(values) == 0 {
		return q
	}
	return q.appendQueryNode("AND", comparisonNode(field, "NOT IN", values...), q.builder())
}

// WhereLike 添加 LIKE 条件。
func (q *Query) WhereLike(field string, pattern string) *Query {
	q = q.clone()
	if err := validateIdentifier(field); err != nil {
		return q.setError(fmt.Errorf("unsafe whereLike field: %w", err))
	}
	return q.appendQueryNode("AND", comparisonNode(field, "LIKE", pattern), q.builder())
}

// WhereNull 添加 IS NULL 条件。
func (q *Query) WhereNull(field string) *Query {
	q = q.clone()
	if err := validateIdentifier(field); err != nil {
		return q.setError(fmt.Errorf("unsafe whereNull field: %w", err))
	}

	return q.appendQueryNode("AND", comparisonNode(field, "IS NULL"), q.builder())
}

// WhereNotNull 添加 IS NOT NULL 条件。
func (q *Query) WhereNotNull(field string) *Query {
	q = q.clone()
	if err := validateIdentifier(field); err != nil {
		return q.setError(fmt.Errorf("unsafe whereNotNull field: %w", err))
	}

	return q.appendQueryNode("AND", comparisonNode(field, "IS NOT NULL"), q.builder())
}

// WhereBetween 添加 BETWEEN 条件。
func (q *Query) WhereBetween(field string, min, max interface{}) *Query {
	q = q.clone()
	if err := validateIdentifier(field); err != nil {
		return q.setError(fmt.Errorf("unsafe whereBetween field: %w", err))
	}
	return q.appendQueryNode("AND", comparisonNode(field, "BETWEEN", min, max), q.builder())
}

// WhereRaw 添加显式原始 SQL 条件。
// 仅应在确实需要复杂表达式时使用，调用方必须自行保证参数化和输入安全。
func (q *Query) WhereRaw(rawSQL string, args ...interface{}) *Query {
	q = q.clone()
	if err := validateRawClause(rawSQL, len(args), "where"); err != nil {
		return q.setError(err)
	}
	return q.appendQueryNode("AND", PredicateNode{Kind: PredicateRaw, SQL: rawSQL, Values: cloneDatabaseValues(args)}, q.builder())
}

// Limit 设置查询数量限制。
func (q *Query) Limit(limit int) *Query {
	q = q.clone()
	if limit < 0 {
		return q.setError(fmt.Errorf("%w: limit 不能为负数", ErrInvalidPagination))
	}
	if limit > maxQueryResultRows {
		return q.setError(fmt.Errorf("%w: limit 超过 %d", ErrInvalidPagination, maxQueryResultRows))
	}
	q.limit = limit
	return q
}

// Offset 设置查询偏移量。
func (q *Query) Offset(offset int) *Query {
	q = q.clone()
	if offset < 0 {
		return q.setError(fmt.Errorf("%w: offset 不能为负数", ErrInvalidPagination))
	}
	q.offset = offset
	return q
}

// Page 根据页码和页大小设置分页参数。
func (q *Query) Page(page, pageSize int) *Query {
	q = q.clone()
	return q.applyPage(page, pageSize)
}

// applyPage 在已经复制出的查询上原地应用分页参数，供组合终端操作复用。
// 公开链式方法仍通过 clone 保证源查询不可变，内部调用可避免重复复制同一状态。
func (q *Query) applyPage(page, pageSize int) *Query {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	if pageSize > maxQueryResultRows {
		return q.setError(fmt.Errorf("%w: pageSize 超过 %d", ErrInvalidPagination, maxQueryResultRows))
	}
	if page > 1 && page-1 > int(^uint(0)>>1)/pageSize {
		return q.setError(fmt.Errorf("%w: page 与 pageSize 乘法溢出", ErrInvalidPagination))
	}
	q.limit = pageSize
	q.offset = (page - 1) * pageSize
	return q
}

func (q *Query) Order(order string) *Query {
	q = q.clone()
	if err := validateOrderClause(order); err != nil {
		return q.setError(fmt.Errorf("unsafe order clause: %w", err))
	}

	q.order = order
	return q
}

// Field 设置查询字段列表。
func (q *Query) Field(fields string) *Query {
	q = q.clone()
	if err := validateIdentifierList(fields); err != nil {
		return q.setError(fmt.Errorf("unsafe field list: %w", err))
	}

	q.fields = fields
	return q
}

// Group 设置 GROUP BY 字段列表。
func (q *Query) Group(group string) *Query {
	q = q.clone()
	if err := validateIdentifierList(group); err != nil {
		return q.setError(fmt.Errorf("unsafe group clause: %w", err))
	}

	q.group = group
	return q
}

// Having 设置默认安全的 HAVING 条件。
// 仅接受参数化条件、NULL 谓词、BETWEEN 谓词以及 field,value 速记写法。
// 如需显式拼接复杂 HAVING 表达式，请调用 HavingRaw。
func (q *Query) Having(having string, args ...interface{}) *Query {
	q = q.clone()
	normalized, err := normalizePredicateClause(having, len(args), "having")
	if err != nil {
		return q.setError(err)
	}
	if !q.replaceHavingArguments(args) {
		return q
	}
	q.having = q.quoteSafeClause(normalized)
	return q
}

// HavingRaw 设置显式原始 HAVING 条件。
// 仅应在聚合函数表达式等无法通过安全入口描述的场景下使用。
func (q *Query) HavingRaw(having string, args ...interface{}) *Query {
	q = q.clone()
	if err := validateRawClause(having, len(args), "having"); err != nil {
		return q.setError(err)
	}
	if !q.replaceHavingArguments(args) {
		return q
	}
	q.having = having
	return q
}

// HavingField 设置安全的 HAVING 条件，自动校验字段名和操作符。
// 示例: HavingField("COUNT(id)", ">", 5) — 注意：聚合函数写法需使用 Having 原始方法。
// 本方法适用于简单字段条件: HavingField("total", ">=", 100)
func (q *Query) HavingField(field string, op string, value interface{}) *Query {
	q = q.clone()
	if err := validateIdentifier(field); err != nil {
		return q.setError(fmt.Errorf("unsafe havingField field: %w", err))
	}
	normalizedOperator, err := normalizeOperator(op)
	if err != nil {
		return q.setError(fmt.Errorf("unsafe havingField operator: %w", err))
	}
	if !q.appendQueryArgument(&q.havingArgs, value) {
		return q
	}
	if q.having != "" {
		q.having += fmt.Sprintf(" AND %s %s ?", q.quoteSafeClause(field), normalizedOperator)
	} else {
		q.having = fmt.Sprintf("%s %s ?", q.quoteSafeClause(field), normalizedOperator)
	}
	return q
}

// Distinct 开启去重查询。
func (q *Query) Distinct() *Query {
	q = q.clone()
	q.distinct = true
	return q
}

// Join 添加 INNER JOIN。
// condition 参数会进行安全校验，仅允许 "table.field = table.field" 格式的等值条件。
func (q *Query) Join(table, condition string) *Query {
	q = q.clone()
	if err := validateIdentifier(table); err != nil {
		return q.setError(fmt.Errorf("unsafe join table: %w", err))
	}
	if err := validateJoinCondition(condition); err != nil {
		return q.setError(fmt.Errorf("unsafe join condition: %w", err))
	}

	q.joins = append(q.joins, joinClause{joinType: "JOIN", table: table, condition: q.quoteSafeClause(condition), rawTable: q.rawTableName})
	return q
}

// LeftJoin 添加 LEFT JOIN。
// condition 参数会进行安全校验。
func (q *Query) LeftJoin(table, condition string) *Query {
	q = q.clone()
	if err := validateIdentifier(table); err != nil {
		return q.setError(fmt.Errorf("unsafe leftJoin table: %w", err))
	}
	if err := validateJoinCondition(condition); err != nil {
		return q.setError(fmt.Errorf("unsafe leftJoin condition: %w", err))
	}

	q.joins = append(q.joins, joinClause{joinType: "LEFT JOIN", table: table, condition: q.quoteSafeClause(condition), rawTable: q.rawTableName})
	return q
}

// RightJoin 添加 RIGHT JOIN。
// condition 参数会进行安全校验。
func (q *Query) RightJoin(table, condition string) *Query {
	q = q.clone()
	if err := validateIdentifier(table); err != nil {
		return q.setError(fmt.Errorf("unsafe rightJoin table: %w", err))
	}
	if err := validateJoinCondition(condition); err != nil {
		return q.setError(fmt.Errorf("unsafe rightJoin condition: %w", err))
	}

	q.joins = append(q.joins, joinClause{joinType: "RIGHT JOIN", table: table, condition: q.quoteSafeClause(condition), rawTable: q.rawTableName})
	return q
}

// Find 查询单条记录。
// Inc 字段自增。
// 对应 ThinkPHP 的 Db::name('user')->where('id', 1)->inc('score', 5)->update()
// 生成的表达式存入 setExprs，在 Update 执行时合并到 SET 子句。
func (q *Query) Inc(field string, step ...int) *Query {
	q = q.clone()
	if err := validateIdentifier(field); err != nil {
		q.setError(fmt.Errorf("unsafe field name for inc: %w", err))
		return q
	}
	amount := 1
	if len(step) > 1 {
		return q.setError(fmt.Errorf("Inc 只能接收一个步长"))
	}
	if len(step) == 1 {
		amount = step[0]
	}
	if amount <= 0 {
		return q.setError(fmt.Errorf("Inc 步长必须大于零"))
	}
	return q.addSetExpression(setExpression{field: field, operator: "+", amount: amount})
}

// Dec 字段自减。
// 对应 ThinkPHP 的 Db::name('user')->where('id', 1)->dec('score', 5)->update()
// 生成的表达式存入 setExprs，在 Update 执行时合并到 SET 子句。
func (q *Query) Dec(field string, step ...int) *Query {
	q = q.clone()
	if err := validateIdentifier(field); err != nil {
		q.setError(fmt.Errorf("unsafe field name for dec: %w", err))
		return q
	}
	amount := 1
	if len(step) > 1 {
		return q.setError(fmt.Errorf("Dec 只能接收一个步长"))
	}
	if len(step) == 1 {
		amount = step[0]
	}
	if amount <= 0 {
		return q.setError(fmt.Errorf("Dec 步长必须大于零"))
	}
	return q.addSetExpression(setExpression{field: field, operator: "-", amount: amount})
}

func (q *Query) addSetExpression(expression setExpression) *Query {
	for _, existing := range q.setExprs {
		if existing.field == expression.field {
			return q.setError(fmt.Errorf("字段 %q 只能配置一个自增或自减表达式", expression.field))
		}
	}
	q.setExprs = append(q.setExprs, expression)
	return q
}

// Lock 添加悲观锁。
// lock(true) 或 lock() 排他锁 FOR UPDATE；lock(false) 共享锁 LOCK IN SHARE MODE。
// 对应 ThinkPHP 的 Db::name('user')->where('id', 1)->lock(true)->find()
// 锁子句作为独立状态存储，构建 SQL 时追加到语句末尾（LIMIT/OFFSET 之后），
// 避免与 ORDER BY 混淆产生非法 SQL。
func (q *Query) Lock(exclusive ...bool) *Query {
	q = q.clone()
	if len(exclusive) > 1 {
		return q.setError(fmt.Errorf("Lock 最多接收一个模式参数"))
	}
	isExclusive := true
	if len(exclusive) > 0 {
		isExclusive = exclusive[0]
	}
	if isExclusive {
		q.lockMode = LockForUpdate
	} else {
		q.lockMode = LockForShare
	}
	return q
}
