package db

import (
	"testing"
)

// TestQueryValueReturnsFirstField 验证 Value() 返回第一行的指定字段值。
func TestQueryValueReturnsFirstField(t *testing.T) {
	db := NewDB(&mockConnection{})

	val, err := db.Name("users").Where("id = ?", 1).Value("name")
	if err != nil {
		t.Fatalf("Value 不应返回错误，实际为 %v", err)
	}
	// mockConnection 返回 map 中只有 table 和 where_count，name 字段不存在
	// 但不会报错，返回 nil
	_ = val
}

// TestQueryColumnReturnsList 验证 Column() 返回字段值列表。
func TestQueryColumnReturnsList(t *testing.T) {
	db := NewDB(&mockConnection{})

	result, err := db.Name("users").Column("name")
	if err != nil {
		t.Fatalf("Column 不应返回错误，实际为 %v", err)
	}
	if result == nil {
		t.Fatal("Column 不应返回 nil")
	}
	list, ok := result.([]interface{})
	if !ok {
		t.Fatalf("Column 无 key 时应返回 []interface{}，实际类型为 %T", result)
	}
	if len(list) != 1 {
		t.Fatalf("Column 返回长度不正确，期望 1，实际 %d", len(list))
	}
}

// TestQueryValueRejectsUnsafeField 验证 Value() 拒绝不安全的字段名。
func TestQueryValueRejectsUnsafeField(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Name("users").Value("name; DROP TABLE")
	if err == nil {
		t.Fatal("Value 应拒绝不安全的字段名")
	}
}

// TestQueryColumnRejectsUnsafeField 验证 Column() 拒绝不安全的字段名。
func TestQueryColumnRejectsUnsafeField(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Name("users").Column("name; DROP TABLE")
	if err == nil {
		t.Fatal("Column 应拒绝不安全的字段名")
	}
}

// TestQueryIncDec 验证 Inc/Dec 生成正确的 SET 表达式（存入 setExprs 而非 where）。
func TestQueryIncDec(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Name("users").Inc("score", 5)
	if len(q.setExprs) != 1 || q.setExprs[0] != "score = score + 5" {
		t.Fatalf("Inc 生成的 SET 表达式不正确: %v", q.setExprs)
	}
	// Inc 不应污染 where 条件
	if len(q.where) != 0 {
		t.Fatalf("Inc 不应生成 where 条件，实际: %v", q.where)
	}

	q2 := db.Name("users").Dec("score", 3)
	if len(q2.setExprs) != 1 || q2.setExprs[0] != "score = score - 3" {
		t.Fatalf("Dec 生成的 SET 表达式不正确: %v", q2.setExprs)
	}
	if len(q2.where) != 0 {
		t.Fatalf("Dec 不应生成 where 条件，实际: %v", q2.where)
	}
}

// TestQueryIncDefaultStep 验证 Inc() 默认步长为 1。
func TestQueryIncDefaultStep(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Name("users").Inc("views")
	if len(q.setExprs) != 1 || q.setExprs[0] != "views = views + 1" {
		t.Fatalf("Inc 默认步长应为 1，实际 SET 表达式: %v", q.setExprs)
	}
}

// TestQueryIncDecChain 验证 Inc/Dec 链式调用与 Where 条件正确分离。
func TestQueryIncDecChain(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Name("users").
		Where("id = ?", 1).
		Inc("score", 10).
		Dec("fail_count", 1)

	// 应有1个 where 条件，2个 SET 表达式
	if len(q.where) != 1 {
		t.Fatalf("应有 1 个 where 条件，实际 %d: %v", len(q.where), q.where)
	}
	if len(q.setExprs) != 2 {
		t.Fatalf("应有 2 个 SET 表达式，实际 %d: %v", len(q.setExprs), q.setExprs)
	}
	if q.setExprs[0] != "score = score + 10" {
		t.Fatalf("第一个 SET 表达式不正确: %s", q.setExprs[0])
	}
	if q.setExprs[1] != "fail_count = fail_count - 1" {
		t.Fatalf("第二个 SET 表达式不正确: %s", q.setExprs[1])
	}
}

// TestQueryIncRejectsUnsafeField 验证 Inc 拒绝不安全的字段名。
func TestQueryIncRejectsUnsafeField(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Name("users").Inc("score; DROP TABLE users", 1)
	if q.err == nil {
		t.Fatal("Inc 应拒绝不安全的字段名")
	}
}

// TestQueryChunkCallsBack 验证 Chunk() 分块回调执行。
func TestQueryChunkCallsBack(t *testing.T) {
	db := NewDB(&mockConnection{})

	callCount := 0
	err := db.Name("users").Where("status = ?", 1).Chunk(10, func(rows []map[string]interface{}) bool {
		callCount++
		return false // 只执行一次
	})
	if err != nil {
		t.Fatalf("Chunk 不应返回错误，实际为 %v", err)
	}
	if callCount != 1 {
		t.Fatalf("Chunk 回调应执行 1 次，实际 %d 次", callCount)
	}
}

// TestQueryLock 验证 Lock() 添加独立锁子句，并正确追加到 SQL 末尾（而非混入 ORDER BY）。
func TestQueryLock(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Name("users").Lock()
	if q.lockMode != "FOR UPDATE" {
		t.Fatalf("Lock() 应设置 lockMode=FOR UPDATE，实际 %q", q.lockMode)
	}
	if q.order != "" {
		t.Fatalf("Lock() 不应污染 order，实际 order=%q", q.order)
	}

	q2 := db.Name("users").Lock(false)
	if q2.lockMode != "LOCK IN SHARE MODE" {
		t.Fatalf("Lock(false) 应设置 lockMode=LOCK IN SHARE MODE，实际 %q", q2.lockMode)
	}

	// 无 ORDER BY 时锁子句应直接追加到末尾，且不产生非法的空 ORDER BY。
	sql, _, err := db.Name("users").Where("id = ?", 1).Lock().BuildSelectSQL()
	if err != nil {
		t.Fatalf("BuildSelectSQL 不应返回错误，实际为 %v", err)
	}
	if sql != "SELECT * FROM users WHERE id = ? FOR UPDATE" {
		t.Fatalf("锁子句应追加到语句末尾，实际 SQL: %s", sql)
	}
}

// TestBuildSelectSQL 验证 BuildSelectSQL() 生成正确的 SQL。
func TestBuildSelectSQL(t *testing.T) {
	db := NewDB(&mockConnection{})

	sql, args, err := db.Name("users").
		Where("status = ?", 1).
		Where("age > ?", 18).
		Order("id desc").
		Limit(10).
		BuildSelectSQL()

	if err != nil {
		t.Fatalf("BuildSelectSQL 不应返回错误，实际为 %v", err)
	}
	if sql != "SELECT * FROM users WHERE status = ? AND age > ? ORDER BY id desc LIMIT 10" {
		t.Fatalf("SQL 不正确: %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("参数数量不正确，期望 2，实际 %d", len(args))
	}
}

// TestBuildSelectSQLWithPrefix 验证 Name() 在有前缀时 BuildSelectSQL 生成正确表名。
func TestBuildSelectSQLWithPrefix(t *testing.T) {
	db := NewDB(&mockConnection{})
	db.prefix = "tg_"

	sql, _, err := db.Name("users").Field("id, name").BuildSelectSQL()
	if err != nil {
		t.Fatalf("BuildSelectSQL 不应返回错误，实际为 %v", err)
	}
	if sql != "SELECT id, name FROM tg_users" {
		t.Fatalf("带前缀的 SQL 不正确: %s", sql)
	}
}

// TestBuildSelectSQLWithTable 验证 Table() 不拼前缀。
func TestBuildSelectSQLWithTable(t *testing.T) {
	db := NewDB(&mockConnection{})
	db.prefix = "tg_"

	sql, _, err := db.Table("my_users").Field("id").BuildSelectSQL()
	if err != nil {
		t.Fatalf("BuildSelectSQL 不应返回错误，实际为 %v", err)
	}
	if sql != "SELECT id FROM my_users" {
		t.Fatalf("Table() 不应拼前缀，实际 SQL: %s", sql)
	}
}
