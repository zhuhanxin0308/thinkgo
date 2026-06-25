package connector

import (
	"strings"
	"testing"

	"thinkgo/framework/db"
)

// newSqliteTestDB 打开一个内存 SQLite 库用于 ORM 集成测试。
func newSqliteTestDB(t *testing.T) *db.DB {
	t.Helper()
	conn, err := (&Sqlite{}).Connect(db.Config{Database: ":memory:"})
	if err != nil {
		t.Skipf("无法打开 SQLite（可能未启用 CGO）：%v", err)
	}
	database := db.NewDB(conn)
	if _, err := database.Execute(`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, status INTEGER)`); err != nil {
		// 未启用 CGO 时 go-sqlite3 为 stub，跳过集成测试而非失败。
		if strings.Contains(err.Error(), "CGO_ENABLED=0") || strings.Contains(err.Error(), "requires cgo") {
			database.Close()
			t.Skipf("跳过 SQLite 集成测试（未启用 CGO）：%v", err)
		}
		t.Fatalf("建表失败: %v", err)
	}
	if _, err := database.Execute(`CREATE TABLE orders (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, amount INTEGER)`); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return database
}

// TestSqliteCountWithJoin 验证带 JOIN 的 Count 返回连接过滤后的真实行数（C1）。
func TestSqliteCountWithJoin(t *testing.T) {
	database := newSqliteTestDB(t)
	defer database.Close()

	if _, err := database.Table("users").Insert(map[string]interface{}{"id": 1, "name": "a", "status": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Table("users").Insert(map[string]interface{}{"id": 2, "name": "b", "status": 1}); err != nil {
		t.Fatal(err)
	}
	// user 1 有 2 个订单，user 2 没有订单。
	database.Table("orders").Insert(map[string]interface{}{"user_id": 1, "amount": 10})
	database.Table("orders").Insert(map[string]interface{}{"user_id": 1, "amount": 20})

	// JOIN 后应只统计有订单的连接行（2 行），而不是 users 表的 2 行（虽然数值巧合，换个数据验证）。
	database.Table("orders").Insert(map[string]interface{}{"user_id": 1, "amount": 30})
	total, err := database.Table("orders").
		Join("users", "orders.user_id = users.id").
		Where("users.status = ?", 1).
		Count()
	if err != nil {
		t.Fatalf("Count 报错: %v", err)
	}
	if total != 3 {
		t.Fatalf("JOIN Count 应为 3（订单数），实际 %d", total)
	}
}

// TestSqliteCountDistinct 验证 DISTINCT 的 Count 统计去重行数（C1）。
func TestSqliteCountDistinct(t *testing.T) {
	database := newSqliteTestDB(t)
	defer database.Close()

	database.Table("orders").Insert(map[string]interface{}{"user_id": 1, "amount": 10})
	database.Table("orders").Insert(map[string]interface{}{"user_id": 1, "amount": 20})
	database.Table("orders").Insert(map[string]interface{}{"user_id": 2, "amount": 30})

	total, err := database.Table("orders").Distinct().Field("user_id").Count()
	if err != nil {
		t.Fatalf("DISTINCT Count 报错: %v", err)
	}
	if total != 2 {
		t.Fatalf("DISTINCT user_id 应为 2，实际 %d", total)
	}
}

// TestSqlitePaginateWithJoin 验证带 JOIN 的分页 total 正确（C1 + Paginate）。
func TestSqlitePaginateWithJoin(t *testing.T) {
	database := newSqliteTestDB(t)
	defer database.Close()

	database.Table("users").Insert(map[string]interface{}{"id": 1, "name": "a", "status": 1})
	for i := 0; i < 5; i++ {
		database.Table("orders").Insert(map[string]interface{}{"user_id": 1, "amount": i})
	}
	p, err := database.Table("orders").
		Join("users", "orders.user_id = users.id").
		Where("users.status = ?", 1).
		Paginate(1, 2)
	if err != nil {
		t.Fatalf("Paginate 报错: %v", err)
	}
	if p.Total != 5 {
		t.Fatalf("JOIN 分页 total 应为 5，实际 %d", p.Total)
	}
	if len(p.List) != 2 {
		t.Fatalf("首页应返回 2 条，实际 %d", len(p.List))
	}
}

// TestSqliteInsertAllBatching 验证大批量 InsertAll 自动分批后全部落库（P4）。
func TestSqliteInsertAllBatching(t *testing.T) {
	database := newSqliteTestDB(t)
	defer database.Close()

	// users 表 3 列 → batchRows = 60000/3 = 20000；插入 20001 行触发 2 批。
	rows := make([]map[string]interface{}, 0, 20001)
	for i := 1; i <= 20001; i++ {
		rows = append(rows, map[string]interface{}{"id": i, "name": "u", "status": 1})
	}
	affected, err := database.Table("users").InsertAll(rows)
	if err != nil {
		t.Fatalf("InsertAll 报错: %v", err)
	}
	if affected != 20001 {
		t.Fatalf("应插入 20001 行，实际 affected=%d", affected)
	}
	total, _ := database.Table("users").Count()
	if total != 20001 {
		t.Fatalf("落库行数应为 20001，实际 %d", total)
	}
}

// TestSqliteChunkById 验证 ChunkById 基于主键游标遍历全部数据（P3）。
func TestSqliteChunkById(t *testing.T) {
	database := newSqliteTestDB(t)
	defer database.Close()

	for i := 1; i <= 25; i++ {
		database.Table("users").Insert(map[string]interface{}{"id": i, "name": "u", "status": 1})
	}
	var count int
	var lastSeen int64
	err := database.Table("users").ChunkById(10, "id", func(rows []map[string]interface{}) bool {
		for _, r := range rows {
			count++
			// id 经 sqlite 返回为 int64。
			if v, ok := r["id"].(int64); ok {
				lastSeen = v
			}
		}
		return true
	})
	if err != nil {
		t.Fatalf("ChunkById 报错: %v", err)
	}
	if count != 25 {
		t.Fatalf("应遍历 25 行，实际 %d", count)
	}
	if lastSeen != 25 {
		t.Fatalf("最后一条主键应为 25，实际 %d", lastSeen)
	}
}
