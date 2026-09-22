package connector

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

var mysqlBenchmarkID atomic.Int64

const (
	mysqlBenchmarkTablePrefix = "bench_orm_users"

	// 基准默认连接池略低于常见本地 MySQL 的 max_connections，避免压测工具自身把服务端连接额度耗尽。
	defaultMySQLBenchmarkMaxOpenConns = 128
	defaultMySQLBenchmarkMaxIdleConns = 32
)

// newMySQLBenchmarkTableName 为每次基准运行生成隔离表，避免清空调用方已有数据。
func newMySQLBenchmarkTableName() string {
	return fmt.Sprintf("%s_%d_%d", mysqlBenchmarkTablePrefix, os.Getpid(), time.Now().UnixNano())
}

// mysqlBenchmarkValueText 将 database/sql 返回的字符串字节值转换为可读文本。
func mysqlBenchmarkValueText(value interface{}) string {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case string:
		return typed
	default:
		return fmt.Sprint(value)
	}
}

// mysqlBenchmarkPoolLimit 读取仅作用于基准进程的连接池上限，拒绝非法值避免静默退回驱动默认值。
func mysqlBenchmarkPoolLimit(b testing.TB, name string, fallback int) int {
	b.Helper()
	raw, exists := os.LookupEnv(name)
	if !exists || strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 {
		b.Fatalf("%s 必须是正整数，实际为 %q", name, raw)
	}
	return value
}

// TestMySQLBenchmarkPoolLimit 验证基准连接池参数可以显式覆盖且空值回退到稳定默认值。
func TestMySQLBenchmarkPoolLimit(t *testing.T) {
	t.Setenv("THINKGO_BENCH_MYSQL_MAX_OPEN_CONNS", "96")
	if got := mysqlBenchmarkPoolLimit(t, "THINKGO_BENCH_MYSQL_MAX_OPEN_CONNS", 12); got != 96 {
		t.Fatalf("基准最大打开连接数覆盖失败，实际为 %d", got)
	}
	t.Setenv("THINKGO_BENCH_MYSQL_MAX_IDLE_CONNS", "")
	if got := mysqlBenchmarkPoolLimit(t, "THINKGO_BENCH_MYSQL_MAX_IDLE_CONNS", 7); got != 7 {
		t.Fatalf("基准空闲连接数空值应回退，实际为 %d", got)
	}
}

// TestMySQLBenchmarkValueText 验证数据库驱动返回的字节字符串不会以数值列表形式记录。
func TestMySQLBenchmarkValueText(t *testing.T) {
	if got := mysqlBenchmarkValueText([]byte("5.6.30")); got != "5.6.30" {
		t.Fatalf("字节字符串转换错误: %q", got)
	}
	if got := mysqlBenchmarkValueText("8.0.30"); got != "8.0.30" {
		t.Fatalf("字符串转换错误: %q", got)
	}
	if got := mysqlBenchmarkValueText(int64(8)); got != "8" {
		t.Fatalf("其他标量转换错误: %q", got)
	}
}

func mysqlBenchmarkEnvironment(b testing.TB) db.Config {
	b.Helper()
	required := []string{
		"THINKGO_BENCH_MYSQL_HOST",
		"THINKGO_BENCH_MYSQL_PORT",
		"THINKGO_BENCH_MYSQL_USER",
		"THINKGO_BENCH_MYSQL_PASSWORD",
		"THINKGO_BENCH_MYSQL_DATABASE",
	}
	values := make(map[string]string, len(required))
	for _, name := range required {
		value, exists := os.LookupEnv(name)
		if !exists {
			b.Skipf("%s 未配置，跳过真实 MySQL ORM 基准", name)
		}
		values[name] = value
	}
	params := map[string]string{"tls": "false"}
	// 仅允许基准进程显式打开客户端参数插值，便于与默认预编译路径做 A/B 对照。
	if strings.EqualFold(strings.TrimSpace(os.Getenv("THINKGO_BENCH_MYSQL_INTERPOLATE_PARAMS")), "true") {
		params["interpolateParams"] = "true"
	}
	maxOpenConns := mysqlBenchmarkPoolLimit(b, "THINKGO_BENCH_MYSQL_MAX_OPEN_CONNS", defaultMySQLBenchmarkMaxOpenConns)
	maxIdleConns := mysqlBenchmarkPoolLimit(b, "THINKGO_BENCH_MYSQL_MAX_IDLE_CONNS", defaultMySQLBenchmarkMaxIdleConns)
	if maxIdleConns > maxOpenConns {
		b.Fatalf("THINKGO_BENCH_MYSQL_MAX_IDLE_CONNS=%d 不能超过 THINKGO_BENCH_MYSQL_MAX_OPEN_CONNS=%d", maxIdleConns, maxOpenConns)
	}
	return db.Config{
		Type:         "mysql",
		Hostname:     values["THINKGO_BENCH_MYSQL_HOST"],
		Hostport:     values["THINKGO_BENCH_MYSQL_PORT"],
		Username:     values["THINKGO_BENCH_MYSQL_USER"],
		Password:     values["THINKGO_BENCH_MYSQL_PASSWORD"],
		Database:     values["THINKGO_BENCH_MYSQL_DATABASE"],
		Params:       params,
		Charset:      "utf8mb4",
		MaxOpenConns: maxOpenConns,
		MaxIdleConns: maxIdleConns,
	}
}

func benchmarkMySQLDatabase(b testing.TB) (*db.DB, *db.SQLConnection, string, string) {
	b.Helper()
	connection, err := (&Mysql{}).Connect(mysqlBenchmarkEnvironment(b))
	if err != nil {
		b.Fatalf("connect real MySQL benchmark database: %v", err)
	}
	sqlConnection, ok := connection.(*db.SQLConnection)
	if !ok {
		connection.Close()
		b.Fatalf("MySQL connector returned %T", connection)
	}
	database := db.NewDB(connection)
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Errorf("close MySQL benchmark database: %v", err)
		}
	})
	tableName := newMySQLBenchmarkTableName()
	createTableSQL := fmt.Sprintf(`CREATE TABLE %s (
		id BIGINT NOT NULL PRIMARY KEY,
		name VARCHAR(128) NOT NULL,
		score INT NOT NULL,
		created_at DATETIME NOT NULL,
		KEY bench_orm_users_score (score)
	) ENGINE=InnoDB`, tableName)
	if _, err := database.Execute(createTableSQL); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if _, err := database.Execute("DROP TABLE IF EXISTS " + tableName); err != nil {
			b.Errorf("drop MySQL benchmark table %s: %v", tableName, err)
		}
	})
	const total = 100_000
	for start := 1; start <= total; start += 1_000 {
		rows := make([]map[string]interface{}, 0, 1_000)
		for index := start; index < start+1_000 && index <= total; index++ {
			rows = append(rows, map[string]interface{}{
				"id": index, "name": "user-" + strconv.Itoa(index), "score": index % 100,
				"created_at": "2026-07-14 00:00:00",
			})
		}
		if affected, err := database.Table(tableName).InsertAll(rows); err != nil || affected != int64(len(rows)) {
			b.Fatalf("seed MySQL rows=%d affected=%d err=%v", len(rows), affected, err)
		}
	}
	versionRows, err := database.Query("SELECT VERSION() AS version")
	if err != nil || len(versionRows) != 1 {
		b.Fatalf("read MySQL version: rows=%#v err=%v", versionRows, err)
	}
	version := mysqlBenchmarkValueText(versionRows[0]["version"])
	mysqlBenchmarkID.Store(1_000_000_000)
	return database, sqlConnection, version, tableName
}

// TestMySQLORMEachReal 验证流式遍历在真实 MySQL 上逐行读取隔离种子，并保持主键顺序。
func TestMySQLORMEachReal(t *testing.T) {
	database, _, _, tableName := benchmarkMySQLDatabase(t)
	var count int
	var previousID int64
	err := database.Table(tableName).Field("id").Order("id ASC").Each(func(row map[string]interface{}) bool {
		id, ok := row["id"].(int64)
		if !ok {
			t.Fatalf("MySQL 流式主键类型错误: %T", row["id"])
		}
		if count > 0 && id <= previousID {
			t.Fatalf("MySQL 流式主键未严格递增: previous=%d current=%d", previousID, id)
		}
		previousID = id
		count++
		return true
	})
	if err != nil {
		t.Fatalf("真实 MySQL 流式读取失败: %v", err)
	}
	if count != 100_000 {
		t.Fatalf("真实 MySQL 流式读取行数错误: %d", count)
	}
	values, err := database.Table(tableName).Order("id ASC").Column("name")
	if err != nil {
		t.Fatalf("真实 MySQL 列扫描失败: %v", err)
	}
	if list, ok := values.([]interface{}); !ok || len(list) != 100_000 {
		t.Fatalf("真实 MySQL 列扫描结果错误: type=%T len=%d", values, len(list))
	}
}

// TestMySQLORMSeekPageReal 验证真实 MySQL 在深游标位置仍通过索引范围读取，而不是 OFFSET。
func TestMySQLORMSeekPageReal(t *testing.T) {
	database, _, _, tableName := benchmarkMySQLDatabase(t)
	page, err := database.Table(tableName).Field("id").SeekPage(1_000, "id", int64(90_000))
	if err != nil {
		t.Fatalf("真实 MySQL 深游标分页失败: %v", err)
	}
	if len(page.List) != 1_000 || page.List[0]["id"] != int64(90_001) || page.List[999]["id"] != int64(91_000) {
		t.Fatalf("真实 MySQL 深游标分页结果错误: first=%#v last=%#v", page.List[0], page.List[len(page.List)-1])
	}
	if page.NextCursor != int64(91_000) || !page.HasMore {
		t.Fatalf("真实 MySQL 深游标分页元数据错误: %#v", page)
	}
}

func runMySQLORMOperation(database *db.DB, tableName string, id int64) error {
	if affected, err := database.Table(tableName).Insert(map[string]interface{}{
		"id": id, "name": "benchmark", "score": id % 100, "created_at": "2026-07-14 00:00:00",
	}); err != nil || affected != 1 {
		return fmt.Errorf("insert affected=%d: %w", affected, err)
	}
	row, err := database.Table(tableName).WhereField("id", "=", id).Find()
	if err != nil || row == nil {
		return fmt.Errorf("select id=%d row=%#v: %w", id, row, err)
	}
	if _, err := database.Table(tableName).WhereField("id", "=", id).Update(map[string]interface{}{"score": 101}); err != nil {
		return fmt.Errorf("update id=%d: %w", id, err)
	}
	if deleted, err := database.Table(tableName).WhereField("id", "=", id).Delete(); err != nil || deleted != 1 {
		return fmt.Errorf("delete id=%d deleted=%d: %w", id, deleted, err)
	}
	return nil
}

func BenchmarkMySQLORM(b *testing.B) {
	database, connection, version, tableName := benchmarkMySQLDatabase(b)
	b.Logf("MySQL version: %s; seeded rows: 100000", version)
	for _, concurrency := range []int{1, 32, 256} {
		b.Run(fmt.Sprintf("concurrency-%d", concurrency), func(b *testing.B) {
			before := connection.DB.Stats()
			var next atomic.Int64
			var failed atomic.Bool
			var failureOnce sync.Once
			var failure error
			var workers sync.WaitGroup
			b.ReportAllocs()
			b.ResetTimer()
			workers.Add(concurrency)
			for worker := 0; worker < concurrency; worker++ {
				go func() {
					defer workers.Done()
					for !failed.Load() {
						operation := next.Add(1)
						if operation > int64(b.N) {
							return
						}
						id := mysqlBenchmarkID.Add(1)
						if err := runMySQLORMOperation(database, tableName, id); err != nil {
							failureOnce.Do(func() {
								failure = err
								failed.Store(true)
							})
							return
						}
					}
				}()
			}
			workers.Wait()
			b.StopTimer()
			if failure != nil {
				b.Fatal(failure)
			}
			after := connection.DB.Stats()
			operations := float64(b.N)
			if operations < 1 {
				operations = 1
			}
			b.ReportMetric(float64(after.WaitCount-before.WaitCount)/operations, "waits/op")
			b.ReportMetric(float64((after.WaitDuration-before.WaitDuration).Nanoseconds())/operations, "wait-ns/op")
		})
	}
}
