package db

import (
	"sync"
	"testing"
)

// mockConnection 模拟数据库连接（用于测试 Query 并发安全性）
type mockConnection struct{}

func (m *mockConnection) Select(table, fields string, where []string, args []interface{}, order string, limit, offset int) ([]map[string]interface{}, error) {
	return []map[string]interface{}{{"table": table, "where_count": len(where)}}, nil
}
func (m *mockConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	return 1, nil
}
func (m *mockConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	return 1, nil
}
func (m *mockConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	return 1, nil
}
func (m *mockConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	return int64(len(where)), nil
}
func (m *mockConnection) Close() error { return nil }

// TestQueryConcurrentSafety 验证 Query 并发安全性
// 100 个 goroutine 同时执行不同表的查询，验证无数据串联
func TestQueryConcurrentSafety(t *testing.T) {
	db := NewDB(&mockConnection{})

	var wg sync.WaitGroup
	errors := make(chan string, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// 每个 goroutine 查询不同的表
			tableName := "table_" + formatIdx(idx)
			q := db.Table(tableName)
			q.Where("id = ?", idx)
			q.Where("status = ?", 1)
			q.Limit(10)

			rows, err := q.Select()
			if err != nil {
				errors <- "select error: " + err.Error()
				return
			}
			if len(rows) == 0 {
				errors <- "no rows returned"
				return
			}

			// 验证返回的表名是否正确（未被其他 goroutine 覆盖）
			returnedTable := rows[0]["table"].(string)
			if returnedTable != tableName {
				errors <- "数据竞争！期望表名 " + tableName + "，实际得到 " + returnedTable
				return
			}

			// 验证 where 条件数量正确（未被其他 goroutine 污染）
			whereCount := rows[0]["where_count"].(int)
			if whereCount != 2 {
				errors <- "数据竞争！期望 2 个 where 条件，实际得到非预期数量"
				return
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Fatal(err)
	}
}

// TestQueryTableReturnsNewInstance 验证 Table() 每次返回新实例
func TestQueryTableReturnsNewInstance(t *testing.T) {
	db := NewDB(&mockConnection{})

	q1 := db.Table("users")
	q2 := db.Table("orders")

	q1.Where("id = ?", 1)

	// q2 不应受 q1 的 where 条件影响
	if len(q2.where) != 0 {
		t.Fatal("Table() 未返回独立实例，q2 的 where 被 q1 污染")
	}

	// q1 的表名不应被 q2 覆盖
	if q1.table != "users" {
		t.Fatal("q1 的表名被 q2 覆盖")
	}
	if q2.table != "orders" {
		t.Fatal("q2 的表名不正确")
	}
}

// TestQueryChainMethods 验证链式调用方法
func TestQueryChainMethods(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").
		Where("status = ?", 1).
		Where("age > ?", 18).
		Order("id desc").
		Limit(10).
		Offset(20).
		Field("id, name, email")

	if q.table != "users" {
		t.Fatal("表名不正确")
	}
	if len(q.where) != 2 {
		t.Fatalf("where 条件数量不正确，期望 2，实际 %d", len(q.where))
	}
	if len(q.args) != 2 {
		t.Fatalf("args 数量不正确，期望 2，实际 %d", len(q.args))
	}
	if q.order != "id desc" {
		t.Fatal("order 不正确")
	}
	if q.limit != 10 {
		t.Fatal("limit 不正确")
	}
	if q.offset != 20 {
		t.Fatal("offset 不正确")
	}
	if q.fields != "id, name, email" {
		t.Fatal("fields 不正确")
	}
}

// TestQueryPage 验证分页方法
func TestQueryPage(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").Page(3, 20)
	if q.limit != 20 {
		t.Fatalf("分页 limit 不正确，期望 20，实际 %d", q.limit)
	}
	if q.offset != 40 {
		t.Fatalf("分页 offset 不正确，期望 40，实际 %d", q.offset)
	}
}

// TestQueryPageDefaults 验证分页方法边界条件
func TestQueryPageDefaults(t *testing.T) {
	db := NewDB(&mockConnection{})

	// 页码 < 1 应该修正为第 1 页
	q := db.Table("users").Page(0, 10)
	if q.offset != 0 {
		t.Fatalf("页码0时 offset 应为0，实际 %d", q.offset)
	}

	// pageSize < 1 应该修正为默认值 20
	q2 := db.Table("users").Page(1, 0)
	if q2.limit != 20 {
		t.Fatalf("pageSize 0 时应修正为默认值 20，实际 %d", q2.limit)
	}
}

// TestQueryWhereIn 验证 WhereIn 条件
func TestQueryWhereIn(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").WhereIn("id", []interface{}{1, 2, 3})
	if len(q.where) != 1 {
		t.Fatal("WhereIn 应生成 1 个 where 条件")
	}
	if q.where[0] != "id IN (?, ?, ?)" {
		t.Fatalf("WhereIn 条件格式不正确: %s", q.where[0])
	}
	if len(q.args) != 3 {
		t.Fatalf("WhereIn args 数量不正确，期望 3，实际 %d", len(q.args))
	}
}

// TestQueryWhereInEmpty 验证空列表的 WhereIn 行为
func TestQueryWhereInEmpty(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").WhereIn("id", []interface{}{})
	// 空列表应添加永假条件，避免返回全部数据
	if len(q.where) != 1 || q.where[0] != "1 = 0" {
		t.Fatal("空 WhereIn 应生成 1 = 0 永假条件")
	}
}

// TestTablePrefix 验证表前缀拼接
func TestTablePrefix(t *testing.T) {
	db := NewDB(&mockConnection{})
	db.prefix = "tg_"

	fullName := db.fullTableName("users")
	if fullName != "tg_users" {
		t.Fatalf("表前缀拼接不正确，期望 tg_users，实际 %s", fullName)
	}
}

// TestTablePrefixEmpty 验证无前缀时不拼接
func TestTablePrefixEmpty(t *testing.T) {
	db := NewDB(&mockConnection{})

	fullName := db.fullTableName("users")
	if fullName != "users" {
		t.Fatalf("无前缀时表名不正确，期望 users，实际 %s", fullName)
	}
}

// TestTableVsNameSemantics 验证 Table() 和 Name() 的语义差异。
// Table() 对应 ThinkPHP 的 Db::table()：使用完整表名，不自动拼接前缀。
// Name() 对应 ThinkPHP 的 Db::name()：自动拼接配置的表前缀。
func TestTableVsNameSemantics(t *testing.T) {
	db := NewDB(&mockConnection{})
	db.prefix = "tg_"

	// Name() 应自动拼前缀
	qName := db.Name("users")
	resolvedName := qName.resolveTable()
	if resolvedName != "tg_users" {
		t.Fatalf("Name() 应自动拼前缀，期望 tg_users，实际 %s", resolvedName)
	}

	// Table() 不应拼前缀（传入的是完整表名）
	qTable := db.Table("tg_users")
	resolvedTable := qTable.resolveTable()
	if resolvedTable != "tg_users" {
		t.Fatalf("Table() 不应拼前缀，期望 tg_users，实际 %s", resolvedTable)
	}

	// 验证 Table() 传入不含前缀的名字也不拼前缀
	qTable2 := db.Table("users")
	resolvedTable2 := qTable2.resolveTable()
	if resolvedTable2 != "users" {
		t.Fatalf("Table() 不应拼前缀，期望 users，实际 %s", resolvedTable2)
	}
}

// formatIdx 格式化索引为字符串（避免引入 strconv）
func formatIdx(idx int) string {
	if idx < 10 {
		return string(rune('0' + idx))
	}
	return string(rune('0'+idx/10)) + string(rune('0'+idx%10))
}

// TestQueryTransactionTableReturnsQuery 验证 tx.Table 和 tx.Name 返回的是 *Query 且 txExecutor 属性正常绑定。
func TestQueryTransactionTableReturnsQuery(t *testing.T) {
	db := NewDB(&mockConnection{})
	tx := &Tx{
		db: db,
		tx: nil, // 模拟 nil 事务对象
	}

	// 验证 Table (不拼前缀)
	q := tx.Table("users")
	var i interface{} = q
	if _, ok := i.(*Query); !ok {
		t.Fatal("tx.Table 应返回 *Query 类型")
	}
	if !q.rawTableName {
		t.Fatal("tx.Table 应标记为 rawTableName")
	}

	// 验证 Name (自动拼前缀)
	qn := tx.Name("users")
	var in interface{} = qn
	if _, ok := in.(*Query); !ok {
		t.Fatal("tx.Name 应返回 *Query 类型")
	}
	if qn.rawTableName {
		t.Fatal("tx.Name 不应标记为 rawTableName")
	}

	// 链式调用后类型依然为 *Query
	q2 := q.Where("id = ?", 1).Limit(10)
	var i2 interface{} = q2
	if _, ok := i2.(*Query); !ok {
		t.Fatal("链式调用后应依然是 *Query")
	}
}
