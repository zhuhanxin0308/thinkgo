package db

// Builder interface for SQL generation
//
// 所有方言统一使用 ? 占位符构建 SQL，由 Rebind 在执行前转换为各驱动要求的占位符风格
// （MySQL/SQLite 使用 ?，PostgreSQL 使用 $N，Oracle 使用 :N，SQL Server 使用 @pN）。
// 这样可避免 WHERE 与 SET 子句占位符风格不一致导致驱动报错。
type Builder interface {
	Select(table string, fields string, where []string, order string, limit int, offset int) string
	Insert(table string, data map[string]interface{}) (string, []interface{})
	Update(table string, data map[string]interface{}, where []string) (string, []interface{})
	Delete(table string, where []string) string
	Count(table string, where []string) string
	// Rebind 将 ? 占位符 SQL 转换为目标方言的占位符风格。
	Rebind(query string) string
	// QuoteIdentifier 按方言引用单个标识符（列/表名），用于手工拼接 SQL 的场景
	// （如批量插入、Inc/Dec 的 SET 子句），保证与 builder 其余路径一致，规避保留字冲突。
	QuoteIdentifier(name string) string
	// Pagination 按方言返回 ORDER BY 子句与分页尾子句（均含前导空格，无内容时为空串）。
	// 复杂查询（JOIN/GROUP/...）的分页统一经此生成，避免硬编码 MySQL 的 LIMIT/OFFSET
	// 在 SQL Server/Oracle 上产生非法 SQL。order 为已校验的排序字段串，可能为空。
	Pagination(order string, limit int, offset int) (orderClause string, limitClause string)
	// LockClause 把统一的悲观锁意图（"FOR UPDATE" / "LOCK IN SHARE MODE"）翻译为方言锁子句
	// （含前导空格）。不支持行级锁的方言返回空串。
	LockClause(mode string) string
	// SupportsLastInsertId 表示驱动是否支持 sql.Result.LastInsertId()。
	// PostgreSQL 等需改用 INSERT ... RETURNING，由连接层据此选择写入路径。
	SupportsLastInsertId() bool
	// InsertReturning 构建带主键回传的 INSERT 语句（用于不支持 LastInsertId 的方言）。
	// primaryKey 为回传的主键列名。不支持该写法的方言可返回 ok=false。
	InsertReturning(table string, data map[string]interface{}, primaryKey string) (query string, values []interface{}, ok bool)
}
