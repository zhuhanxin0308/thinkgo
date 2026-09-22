package db

import "github.com/zhuhanxin0308/thinkgo/v3/db/internal/contract"

type LockMode = contract.LockMode

const (
	LockNone      = contract.LockNone
	LockForUpdate = contract.LockForUpdate
	LockForShare  = contract.LockForShare
)

type LockSpec = contract.LockSpec

// Builder interface for SQL generation
//
// 所有方言统一使用 ? 占位符构建 SQL，由 Rebind 在执行前转换为各驱动要求的占位符风格
// （MySQL/SQLite 使用 ?，PostgreSQL 使用 $N，Oracle 使用 :N，SQL Server 使用 @pN）。
// 这样可避免 WHERE 与 SET 子句占位符风格不一致导致驱动报错。
type Builder interface {
	// DialectName returns the stable SQL dialect identifier used by validation and redaction.
	DialectName() string
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
	// QuoteFields 引用逗号分隔字段、AS 别名和受支持聚合表达式。
	QuoteFields(fields string) string
	// Pagination 按方言返回 ORDER BY 子句与分页尾子句（均含前导空格，无内容时为空串）。
	// 复杂查询（JOIN/GROUP/...）的分页统一经此生成，避免硬编码 MySQL 的 LIMIT/OFFSET
	// 在 SQL Server/Oracle 上产生非法 SQL。order 为已校验的排序字段串，可能为空。
	Pagination(order string, limit int, offset int) (orderClause string, limitClause string)
	// Lock 把类型化悲观锁意图翻译为方言锁规格或显式 capability error，
	// 不支持的驱动不得静默忽略锁意图。
	Lock(mode LockMode) (LockSpec, error)
	// SupportsLastInsertId 表示驱动是否支持 sql.Result.LastInsertId()。
	// PostgreSQL 等需改用 INSERT ... RETURNING，由连接层据此选择写入路径。
	SupportsLastInsertId() bool
	// MaxBindParams 返回单条语句允许的绑定参数预算，供批量写入安全分批。
	MaxBindParams() int
	// InsertReturning 构建带主键回传的 INSERT 语句（用于不支持 LastInsertId 的方言）。
	// primaryKey 为回传的主键列名。不支持该写法的方言可返回 ok=false。
	InsertReturning(table string, data map[string]interface{}, primaryKey string) (query string, values []interface{}, ok bool)
}

// BatchInsertBuilder 由不能使用通用多行 VALUES 语法的方言选择性实现。
// fields 已完成校验并按稳定顺序排列，rows 中每行具有完全相同的字段集合。
type BatchInsertBuilder interface {
	InsertBatch(table string, fields []string, rows []map[string]interface{}) (query string, values []interface{})
}

// BatchStatementSizer 为需要额外控制单条批量语句大小的方言提供可选能力。
// 未实现该接口的方言继续只按 MaxBindParams 分批，避免把某个数据库的协议限制
// 错误套用到其它数据库。
type BatchStatementSizer interface {
	// MaxBatchStatementBytes 返回批量语句的保守字节预算；返回不大于零表示不启用预算。
	MaxBatchStatementBytes() int
}

// BatchRowLimiter 表达独立于参数数量的批量语法行数限制，不改变现有 Builder 实现契约。
type BatchRowLimiter interface {
	// MaxBatchRows 返回单条批量语句的最大行数；不大于零表示没有额外限制。
	MaxBatchRows() int
}
