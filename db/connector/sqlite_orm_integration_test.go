package connector

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

func TestSqliteMemoryDatabaseSurvivesConfiguredExpiry(t *testing.T) {
	connection, err := (&Sqlite{}).Connect(db.Config{
		Database:               ":memory:",
		ConnMaxLifetimeSeconds: 1,
		ConnMaxIdleTimeSeconds: 1,
	})
	if err != nil {
		if strings.Contains(err.Error(), "CGO_ENABLED=0") || strings.Contains(err.Error(), "requires cgo") {
			t.Skipf("skip SQLite memory expiry test without CGO: %v", err)
		}
		t.Fatalf("open SQLite memory database: %v", err)
	}
	database := db.NewDB(connection)
	defer database.Close()
	if _, err := database.Execute("CREATE TABLE keepalive (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("create SQLite memory table: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := database.Query("SELECT id FROM keepalive"); err != nil {
		t.Fatalf("SQLite memory database was lost after configured expiry: %v", err)
	}
}

// TestSqliteNativeTimeUsesApplicationTimezone 验证 SQLite 原生时间读写与查询使用同一应用时区。
func TestSqliteNativeTimeUsesApplicationTimezone(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}
	connection, err := (&Sqlite{}).Connect(db.Config{
		Database: ":memory:",
		Params:   map[string]string{"_loc": location.String()},
	})
	if err != nil {
		t.Skipf("无法打开 SQLite（可能未启用 CGO）：%v", err)
	}
	database := db.NewDB(connection)
	defer database.Close()
	database.SetLocation(location)
	if _, err := database.Execute(`CREATE TABLE events (id INTEGER PRIMARY KEY, happened_at DATETIME)`); err != nil {
		t.Fatalf("创建 SQLite 时间表失败: %v", err)
	}

	instant := time.Date(2026, time.July, 24, 16, 30, 0, 123000000, time.UTC)
	if _, err := database.Table("events").Insert(map[string]interface{}{"id": 1, "happened_at": instant}); err != nil {
		t.Fatalf("写入 SQLite 原生时间失败: %v", err)
	}
	row, err := database.Table("events").WhereTimeAs("happened_at", db.TimestampValueTypeNative, "=", instant).Find()
	if err != nil {
		t.Fatalf("按原生时间查询 SQLite 失败: %v", err)
	}
	actual, ok := row["happened_at"].(time.Time)
	if !ok || !actual.Equal(instant) || actual.Location() != location {
		t.Fatalf("SQLite 原生时间未归一化到应用时区: %#v", row["happened_at"])
	}
}

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

	// users 表 3 列、SQLite 参数预算 999 → 每批 333 行；插入 20001 行会稳定触发多批事务。
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

// TestSqliteModelEach 验证模型流式读取会应用获取器，并支持提前停止而不物化剩余行。
func TestSqliteModelEach(t *testing.T) {
	database := newSqliteTestDB(t)
	defer database.Close()
	for i := 1; i <= 5; i++ {
		if _, err := database.Table("users").Insert(map[string]interface{}{"id": i, "name": "user", "status": 1}); err != nil {
			t.Fatal(err)
		}
	}
	model := db.NewModel(database, "users")
	if err := model.Getter("name", func(value interface{}, _ map[string]interface{}) interface{} {
		return value.(string) + "-loaded"
	}); err != nil {
		t.Fatal(err)
	}
	seen := 0
	err := model.Order("id").Each(func(row map[string]interface{}) bool {
		seen++
		if row["name"] != "user-loaded" {
			t.Fatalf("Each 未应用模型获取器: %#v", row)
		}
		return seen < 2
	})
	if err != nil {
		t.Fatalf("模型 Each 失败: %v", err)
	}
	if seen != 2 {
		t.Fatalf("模型 Each 提前停止错误: seen=%d", seen)
	}
}

// TestSqliteColumnProjectsRequestedField 验证 Column 仍只返回请求字段的值集合。
func TestSqliteColumnProjectsRequestedField(t *testing.T) {
	database := newSqliteTestDB(t)
	defer database.Close()
	for i := 1; i <= 3; i++ {
		if _, err := database.Table("users").Insert(map[string]interface{}{"id": i, "name": "user", "status": i}); err != nil {
			t.Fatal(err)
		}
	}
	values, err := database.Table("users").Order("id").Column("name")
	if err != nil || !reflect.DeepEqual(values, []interface{}{"user", "user", "user"}) {
		t.Fatalf("Column 结果错误: values=%#v err=%v", values, err)
	}
	keyed, err := database.Table("users").Order("id").Column("name", "id")
	if err != nil || !reflect.DeepEqual(keyed, map[string]interface{}{"1": "user", "2": "user", "3": "user"}) {
		t.Fatalf("带 key 的 Column 结果错误: values=%#v err=%v", keyed, err)
	}
	if _, err := database.Table("users").Column("name", "name"); !errors.Is(err, db.ErrInvalidDatabaseRow) {
		t.Fatalf("重复列应返回 ErrInvalidDatabaseRow: %v", err)
	}
}

// TestSqliteSeekPage 验证 SQLite 真实查询使用主键游标连续读取页面。
func TestSqliteSeekPage(t *testing.T) {
	database := newSqliteTestDB(t)
	defer database.Close()

	for i := 1; i <= 25; i++ {
		if _, err := database.Table("users").Insert(map[string]interface{}{"id": i, "name": "u", "status": 1}); err != nil {
			t.Fatal(err)
		}
	}
	query := database.Table("users").Field("id,name")
	first, err := query.SeekPage(10, "id", nil)
	if err != nil {
		t.Fatalf("SQLite 首页游标分页失败: %v", err)
	}
	if len(first.List) != 10 || first.NextCursor != int64(10) || !first.HasMore {
		t.Fatalf("SQLite 首页游标分页结果错误: %#v", first)
	}

	second, err := query.SeekPage(10, "id", first.NextCursor)
	if err != nil {
		t.Fatalf("SQLite 第二页游标分页失败: %v", err)
	}
	if len(second.List) != 10 || second.List[0]["id"] != int64(11) || second.List[9]["id"] != int64(20) || second.NextCursor != int64(20) || !second.HasMore {
		t.Fatalf("SQLite 第二页游标分页结果错误: %#v", second)
	}

	third, err := query.SeekPage(10, "id", second.NextCursor)
	if err != nil {
		t.Fatalf("SQLite 末页游标分页失败: %v", err)
	}
	if len(third.List) != 5 || third.List[0]["id"] != int64(21) || third.List[4]["id"] != int64(25) || third.NextCursor != nil || third.HasMore {
		t.Fatalf("SQLite 末页游标分页结果错误: %#v", third)
	}
}

func TestSQLiteSimplePathAggregates(t *testing.T) {
	database := newSqliteTestDB(t)
	defer database.Close()
	for _, amount := range []int{10, 20, 30} {
		if _, err := database.Table("orders").Insert(map[string]interface{}{"user_id": 1, "amount": amount}); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name string
		run  func(*db.Query) (float64, error)
		want float64
	}{
		{name: "sum", run: func(query *db.Query) (float64, error) { return query.Sum("amount") }, want: 60},
		{name: "avg", run: func(query *db.Query) (float64, error) { return query.Avg("amount") }, want: 20},
		{name: "min", run: func(query *db.Query) (float64, error) { return query.Min("amount") }, want: 10},
		{name: "max", run: func(query *db.Query) (float64, error) { return query.Max("amount") }, want: 30},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := testCase.run(database.Table("orders"))
			if err != nil || got != testCase.want {
				t.Fatalf("aggregate=%v want=%v err=%v", got, testCase.want, err)
			}
		})
	}
}
