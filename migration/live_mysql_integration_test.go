//go:build integration

package migration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/db/connector"
)

// newLiveMySQLMigrationStore 只允许独立测试库，且拒绝接管已有迁移表。
func newLiveMySQLMigrationStore(t *testing.T) (*db.DB, *DatabaseStore) {
	t.Helper()
	host, configured := os.LookupEnv("THINKGO_LIVE_MYSQL_HOST")
	if !configured {
		t.Skip("THINKGO_LIVE_MYSQL_* 未配置")
	}
	for _, suffix := range []string{"PORT", "DATABASE", "USER", "PASSWORD"} {
		if _, exists := os.LookupEnv("THINKGO_LIVE_MYSQL_" + suffix); !exists {
			t.Fatalf("真实 MySQL 环境缺少 %s", suffix)
		}
	}
	name := os.Getenv("THINKGO_LIVE_MYSQL_DATABASE")
	if name != "thinkgo_live" && !strings.HasPrefix(name, "thinkgo_test_") {
		t.Fatal("迁移真实测试仅允许 thinkgo_live 或 thinkgo_test_ 前缀的独立测试库")
	}
	connection, err := (&connector.Mysql{}).Connect(db.Config{
		Type: "mysql", Hostname: host, Hostport: os.Getenv("THINKGO_LIVE_MYSQL_PORT"),
		Database: name, Username: os.Getenv("THINKGO_LIVE_MYSQL_USER"), Password: os.Getenv("THINKGO_LIVE_MYSQL_PASSWORD"),
		Charset: "utf8mb4", Params: map[string]string{"tls": "false"}, MaxOpenConns: 4, MaxIdleConns: 2,
	})
	if err != nil {
		t.Fatalf("真实 MySQL 连接失败: %v", err)
	}
	database := db.NewDB(connection)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("关闭真实数据库失败: %v", err)
		}
	})
	tables := []string{migrationHistoryTable, migrationJournalTable, migrationFenceTable}
	for _, table := range tables {
		rows, err := database.Query("SELECT 1 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", table)
		if err != nil || len(rows) != 0 {
			t.Fatalf("拒绝接管已有迁移表 %s 或无法检查其状态: %v", table, err)
		}
	}
	t.Cleanup(func() {
		for _, table := range tables {
			if _, err := database.Execute("DROP TABLE IF EXISTS " + table); err != nil {
				t.Errorf("清理本次创建的迁移表失败: %v", err)
			}
		}
	})
	store, err := NewDatabaseStore(database)
	if err != nil {
		t.Fatal(err)
	}
	return database, store
}

// TestLiveMySQLMigrationRecovery 验证 MySQL 隐式提交 DDL 下的历史、回滚与失败恢复边界。
func TestLiveMySQLMigrationRecovery(t *testing.T) {
	database, store := newLiveMySQLMigrationStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	table := fmt.Sprintf("tg_live_migration_%d_%d", os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := database.Execute("DROP TABLE IF EXISTS " + table); err != nil {
			t.Errorf("清理本次迁移业务表失败: %v", err)
		}
	})
	newRunner := func(name string, up []string) *Runner {
		t.Helper()
		current, err := NewSQL(name, up, []string{"DROP TABLE " + table})
		if err != nil {
			t.Fatal(err)
		}
		registry := NewRegistry()
		if err := registry.Register(current); err != nil {
			t.Fatal(err)
		}
		runner, err := NewRunner(registry, store)
		if err != nil {
			t.Fatal(err)
		}
		return runner
	}
	create := "CREATE TABLE " + table + " (id BIGINT PRIMARY KEY, name VARCHAR(64) NOT NULL)"
	runner := newRunner("202609090001_live_create", []string{create})
	if result, err := runner.Apply(ctx); err != nil || len(result.Names) != 1 {
		t.Fatalf("真实迁移失败: result=%#v err=%v", result, err)
	}
	if history, err := store.Applied(ctx); err != nil || len(history) != 1 || history[0].Checksum == "" {
		t.Fatalf("迁移历史没有完整落库: history=%#v err=%v", history, err)
	}
	if result, err := runner.Apply(ctx); err != nil || len(result.Names) != 0 {
		t.Fatalf("重复迁移不幂等: result=%#v err=%v", result, err)
	}
	if result, err := runner.Rollback(ctx, 1); err != nil || len(result.Names) != 1 {
		t.Fatalf("真实回滚失败: result=%#v err=%v", result, err)
	}
	if rows, err := database.Query("SELECT 1 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", table); err != nil || len(rows) != 0 {
		t.Fatalf("回滚未移除本次业务表: rows=%v err=%v", rows, err)
	}
	failedName := "202609090002_live_partial_failure"
	failed := newRunner(failedName, []string{create, create})
	if _, err := failed.Apply(ctx); err == nil {
		t.Fatal("重复 DDL 应失败")
	}
	journal, err := store.Journal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var failure *JournalEntry
	for index := range journal {
		if journal[index].Name == failedName {
			failure = &journal[index]
		}
	}
	if failure == nil || failure.State != JournalFailed || failure.FencingToken <= 0 || failure.FailureCode == "" {
		t.Fatalf("隐式提交后的部分失败缺少持久恢复信息: %#v", failure)
	}
	if history, err := store.Applied(ctx); err != nil || len(history) != 0 {
		t.Fatalf("部分失败不得记录为成功迁移: history=%#v err=%v", history, err)
	}
	// MySQL 已提交第一条 DDL，恢复前必须拒绝再次执行，并保留现场供显式恢复处理。
	if _, err := failed.Apply(ctx); err == nil {
		t.Fatal("未解决的失败迁移不能自动重放")
	}
	if _, err := database.Execute("DROP TABLE " + table); err != nil {
		t.Fatalf("撤销本次测试的部分 DDL 失败: %v", err)
	}
	if err := failed.ResolveDirty(ctx, failedName, RecoveryMarkReverted); err != nil {
		t.Fatalf("明确回退实际 DDL 后无法恢复迁移状态: %v", err)
	}
}
