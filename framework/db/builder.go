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
}
