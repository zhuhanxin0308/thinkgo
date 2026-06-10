package db

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
