//go:build integration && oracle

package migration

import (
	"os"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/connector"
)

func TestLiveOracleDataMigrationsRollbackAllStatements(t *testing.T) {
	assertLiveDataMigrationRollback(t, newLiveOracleMigrationStore, "NUMBER(19)", "", "ORA-00001")
}

func newLiveOracleMigrationStore(t *testing.T) (*db.DB, *DatabaseStore) {
	t.Helper()
	const prefix = "THINKGO_LIVE_ORACLE_"
	host := os.Getenv(prefix + "HOST")
	if host == "" {
		t.Skip("未配置独立 Oracle 环境")
	}
	user := os.Getenv(prefix + "USER")
	if !strings.HasPrefix(strings.ToLower(user), "thinkgo_test_") {
		t.Fatal("Oracle 迁移测试仅允许 thinkgo_test_ 前缀的独立测试账号")
	}
	connection, err := (&connector.Oracle{}).Connect(db.Config{
		Type: "oracle", Hostname: host, Hostport: os.Getenv(prefix + "PORT"), Database: os.Getenv(prefix + "DATABASE"),
		Username: user, Password: os.Getenv(prefix + "PASSWORD"), Params: map[string]string{"protocol": "tcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	database := db.NewDB(connection)
	t.Cleanup(func() { _ = database.Close() })
	tables := []string{migrationHistoryTable, migrationJournalTable, migrationFenceTable}
	for _, table := range tables {
		rows, err := database.Query("SELECT 1 FROM user_tables WHERE table_name = ?", strings.ToUpper(table))
		if err != nil || len(rows) != 0 {
			t.Fatalf("拒绝接管已有迁移表 %s 或无法检查其状态: %v", table, err)
		}
	}
	t.Cleanup(func() {
		for _, table := range tables {
			rows, err := database.Query("SELECT 1 FROM user_tables WHERE table_name = ?", strings.ToUpper(table))
			if err != nil {
				t.Error(err)
				continue
			}
			if len(rows) != 0 {
				if _, err := database.Execute("DROP TABLE " + table); err != nil {
					t.Error(err)
				}
			}
		}
	})
	store, err := NewDatabaseStore(database)
	if err != nil {
		t.Fatal(err)
	}
	return database, store
}
