package db

import "context"

// Connection 数据库连接接口
// 所有数据库驱动（MySQL/PostgreSQL/MongoDB/Neo4j）都需实现此接口
type Connection interface {
	// Select 查询多条记录
	Select(table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error)
	// Insert 插入记录，返回最后插入 ID
	Insert(table string, data map[string]interface{}) (int64, error)
	// Update 更新记录，返回影响行数
	Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error)
	// Delete 删除记录，返回影响行数
	Delete(table string, where []string, args []interface{}) (int64, error)
	// Count 统计记录数
	Count(table string, where []string, args []interface{}) (int64, error)
	// Close 关闭连接
	Close() error
}

// RawQueryable 原生 SQL 查询接口（SQL 数据库专用）
// 对应 ThinkPHP 8 的 Db::query() 和 Db::execute()
type RawQueryable interface {
	// Query 执行原生 SQL 查询，返回结果集
	Query(sql string, args ...interface{}) ([]map[string]interface{}, error)
	// Execute 执行原生 SQL 命令（INSERT/UPDATE/DELETE/DDL），返回影响行数
	Execute(sql string, args ...interface{}) (int64, error)
}

// ContextualConnection 是可选的、支持 context 的连接接口。
// 实现它的连接（如 SQLConnection）在 Query 设置 WithContext 后，
// 底层查询将随上下文超时/取消，避免慢查询在请求结束后仍占用连接池。
// 未实现该接口的连接（部分测试桩/特殊驱动）自动回退到无 context 的方法。
type ContextualConnection interface {
	SelectContext(ctx context.Context, table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error)
	InsertContext(ctx context.Context, table string, data map[string]interface{}) (int64, error)
	UpdateContext(ctx context.Context, table string, data map[string]interface{}, where []string, args []interface{}) (int64, error)
	DeleteContext(ctx context.Context, table string, where []string, args []interface{}) (int64, error)
	CountContext(ctx context.Context, table string, where []string, args []interface{}) (int64, error)
}

// ContextualRawQueryable 是可选的、支持 context 的原生 SQL 接口。
type ContextualRawQueryable interface {
	QueryContext(ctx context.Context, sql string, args ...interface{}) ([]map[string]interface{}, error)
	ExecuteContext(ctx context.Context, sql string, args ...interface{}) (int64, error)
}
