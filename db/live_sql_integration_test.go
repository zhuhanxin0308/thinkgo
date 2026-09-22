//go:build integration

package db_test

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/db/connector"
)

var liveSQLSequence atomic.Uint64

func liveSQLConfigFromEnv(driver string) (db.Config, bool) {
	prefix := "THINKGO_LIVE_MYSQL_"
	params := map[string]string{"tls": "false"}
	if driver == "pgsql" {
		prefix = "THINKGO_LIVE_PGSQL_"
		params = map[string]string{"sslmode": "disable"}
	}
	required := []string{"HOST", "PORT", "DATABASE", "USER", "PASSWORD"}
	values := make(map[string]string, len(required))
	for _, suffix := range required {
		value, exists := os.LookupEnv(prefix + suffix)
		if !exists {
			return db.Config{}, false
		}
		values[suffix] = value
	}
	return db.Config{
		Type:         driver,
		Hostname:     values["HOST"],
		Hostport:     values["PORT"],
		Database:     values["DATABASE"],
		Username:     values["USER"],
		Password:     values["PASSWORD"],
		Params:       params,
		Charset:      "utf8mb4",
		MaxOpenConns: 8,
		MaxIdleConns: 4,
	}, true
}

func connectLiveSQL(tb testing.TB, driver string) *db.DB {
	tb.Helper()
	config, configured := liveSQLConfigFromEnv(driver)
	if !configured {
		tb.Skipf("%s 真实服务环境变量未配置", driver)
	}
	var databaseConnector db.Connector
	switch driver {
	case "mysql":
		databaseConnector = &connector.Mysql{}
	case "pgsql":
		databaseConnector = &connector.Pgsql{}
	default:
		tb.Fatalf("不支持的真实 SQL 测试驱动 %q", driver)
	}
	connection, err := databaseConnector.Connect(config)
	if err != nil {
		tb.Fatalf("连接真实 %s 服务失败: %v", driver, err)
	}
	database := db.NewDB(connection)
	tb.Cleanup(func() {
		if closeErr := database.Close(); closeErr != nil {
			tb.Errorf("关闭真实 %s 连接失败: %v", driver, closeErr)
		}
	})
	return database
}

func liveSQLTableName() string {
	return fmt.Sprintf("tg_live_%d_%d_%d", os.Getpid(), time.Now().UnixNano(), liveSQLSequence.Add(1))
}

func createLiveSQLTable(tb testing.TB, database *db.DB, driver string) string {
	tb.Helper()
	table := liveSQLTableName()
	createStatement := fmt.Sprintf(`CREATE TABLE %s (
		id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(128) NOT NULL,
		score INTEGER NOT NULL,
		active BOOLEAN NOT NULL,
		created_at DATETIME(6) NOT NULL
	)`, table)
	if driver == "pgsql" {
		createStatement = fmt.Sprintf(`CREATE TABLE %s (
			id BIGSERIAL PRIMARY KEY,
			name VARCHAR(128) NOT NULL,
			score INTEGER NOT NULL,
			active BOOLEAN NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		)`, table)
	}
	if _, err := database.Execute(createStatement); err != nil {
		tb.Fatalf("创建真实 %s 测试表失败: %v", driver, err)
	}
	tb.Cleanup(func() {
		if _, err := database.Execute("DROP TABLE IF EXISTS " + table); err != nil {
			tb.Errorf("清理真实 %s 测试表失败: %v", driver, err)
		}
	})
	return table
}

func liveSQLInt64(value interface{}) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	case string:
		return strconv.ParseInt(typed, 10, 64)
	default:
		return 0, fmt.Errorf("无法把 %T 转换为 int64", value)
	}
}

func liveSQLText(value interface{}) string {
	if bytes, ok := value.([]byte); ok {
		return string(bytes)
	}
	return fmt.Sprint(value)
}

func assertLiveSQLQueryAndTransactionContract(t *testing.T, driver string) {
	t.Helper()
	database := connectLiveSQL(t, driver)
	table := createLiveSQLTable(t, database, driver)
	instant := time.Date(2026, time.August, 27, 12, 34, 56, 123_000_000, time.UTC)

	insertedID, err := database.Table(table).InsertGetId(map[string]interface{}{
		"name": "Ada", "score": 10, "active": true, "created_at": instant,
	})
	if err != nil {
		t.Fatalf("真实 %s InsertGetId 失败: %v", driver, err)
	}
	id, err := liveSQLInt64(insertedID)
	if err != nil || id < 1 {
		t.Fatalf("真实 %s 回传主键非法: id=%#v err=%v", driver, insertedID, err)
	}

	rows := []map[string]interface{}{
		{"name": "Grace", "score": 20, "active": true, "created_at": instant.Add(time.Second)},
		{"name": "Lin", "score": 30, "active": false, "created_at": instant.Add(2 * time.Second)},
	}
	if affected, insertErr := database.Table(table).InsertAll(rows); insertErr != nil || affected != int64(len(rows)) {
		t.Fatalf("真实 %s 批量写入失败: affected=%d err=%v", driver, affected, insertErr)
	}

	record, err := database.Table(table).WhereField("id", "=", id).Find()
	if err != nil || record == nil || liveSQLText(record["name"]) != "Ada" {
		t.Fatalf("真实 %s 主键读取失败: record=%#v err=%v", driver, record, err)
	}
	if score, scoreErr := liveSQLInt64(record["score"]); scoreErr != nil || score != 10 {
		t.Fatalf("真实 %s 数值映射错误: score=%#v err=%v", driver, record["score"], scoreErr)
	}
	if _, ok := record["created_at"].(time.Time); !ok {
		t.Fatalf("真实 %s 时间映射应为 time.Time，实际为 %T", driver, record["created_at"])
	}

	if affected, updateErr := database.Table(table).WhereField("id", "=", id).Update(map[string]interface{}{"score": 11}); updateErr != nil || affected != 1 {
		t.Fatalf("真实 %s 更新失败: affected=%d err=%v", driver, affected, updateErr)
	}
	var streamed int
	if eachErr := database.Table(table).Order("id ASC").Each(func(row map[string]interface{}) bool {
		streamed++
		return liveSQLText(row["name"]) != ""
	}); eachErr != nil || streamed != 3 {
		t.Fatalf("真实 %s 流式读取失败: rows=%d err=%v", driver, streamed, eachErr)
	}

	rollbackMarker := errors.New("验证事务回滚")
	err = database.Transaction(func(transaction *db.Tx) error {
		_, insertErr := transaction.Table(table).Insert(map[string]interface{}{
			"name": "Rollback", "score": 40, "active": true, "created_at": instant,
		})
		if insertErr != nil {
			return insertErr
		}
		return rollbackMarker
	})
	if !errors.Is(err, rollbackMarker) {
		t.Fatalf("真实 %s 事务回滚未保留业务错误: %v", driver, err)
	}
	if count, countErr := database.Table(table).WhereField("name", "=", "Rollback").Count(); countErr != nil || count != 0 {
		t.Fatalf("真实 %s 回滚记录仍然存在: count=%d err=%v", driver, count, countErr)
	}

	err = database.Transaction(func(transaction *db.Tx) error {
		_, insertErr := transaction.Table(table).Insert(map[string]interface{}{
			"name": "Commit", "score": 50, "active": true, "created_at": instant,
		})
		return insertErr
	})
	if err != nil {
		t.Fatalf("真实 %s 事务提交失败: %v", driver, err)
	}
	if count, countErr := database.Table(table).WhereField("name", "=", "Commit").Count(); countErr != nil || count != 1 {
		t.Fatalf("真实 %s 提交记录不可见: count=%d err=%v", driver, count, countErr)
	}
	if affected, deleteErr := database.Table(table).WhereField("id", "=", id).Delete(); deleteErr != nil || affected != 1 {
		t.Fatalf("真实 %s 删除失败: affected=%d err=%v", driver, affected, deleteErr)
	}
}

// TestLiveMySQLQueryAndTransactionContract 验证真实 MySQL 的主键回传、批量写入、类型映射、流式读取与事务语义。
func TestLiveMySQLQueryAndTransactionContract(t *testing.T) {
	assertLiveSQLQueryAndTransactionContract(t, "mysql")
}

// TestLivePostgreSQLQueryAndTransactionContract 验证真实 PostgreSQL 的 RETURNING、批量写入、类型映射、流式读取与事务语义。
func TestLivePostgreSQLQueryAndTransactionContract(t *testing.T) {
	assertLiveSQLQueryAndTransactionContract(t, "pgsql")
}
