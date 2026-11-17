//go:build integration

package migration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

func TestLiveMySQLDataMigrationsRollbackAllStatements(t *testing.T) {
	assertLiveDataMigrationRollback(t, newLiveMySQLMigrationStore, "BIGINT", " ENGINE=InnoDB", "Error 1062")
}

func TestLiveMySQLDataMigrationRejectsLegacyMyISAMJournal(t *testing.T) {
	database, store := newLiveMySQLMigrationStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if err := store.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecuteContext(ctx, "ALTER TABLE "+migrationJournalTable+" ENGINE=MyISAM"); err != nil {
		t.Fatal(err)
	}
	current, err := NewSQL("202611160001_non_transactional_journal",
		[]string{"INSERT INTO " + migrationFenceTable + " (lock_name, fencing_token) VALUES ('business_test', 1)"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = store.WithMigrationLock(ctx, func(bound Store) error {
		return bound.Apply(ctx, current, 1)
	})
	if !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("MyISAM 元表必须在写入前被拒绝: %v", err)
	}
	if count, err := database.Table(migrationFenceTable).Where("lock_name = ?", "business_test").Count(); err != nil || count != 0 {
		t.Fatalf("拒绝前不得执行业务写入: %d %v", count, err)
	}
	if rows, err := store.Journal(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("拒绝前不得落 applying 日志: %#v %v", rows, err)
	}
}

func assertLiveDataMigrationRollback(t *testing.T, fixture func(*testing.T) (*db.DB, *DatabaseStore), idType, options, duplicateKeyCode string) {
	t.Helper()
	for _, revert := range []bool{false, true} {
		t.Run(fmt.Sprintf("回滚=%t", revert), func(t *testing.T) {
			database, store := fixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			table := fmt.Sprintf("tg_dml_%d_%d", os.Getpid(), time.Now().UnixNano())
			if _, err := database.ExecuteContext(ctx, "CREATE TABLE "+table+" (id "+idType+" PRIMARY KEY)"+options); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := database.Execute("DROP TABLE " + table); err != nil {
					t.Error(err)
				}
			})
			insertOriginal := "INSERT INTO " + table + " (id) VALUES (1)"
			insertDuplicate := "INSERT INTO " + table + " (id) VALUES (2)"
			up := []string{insertOriginal, insertOriginal}
			var down []string
			if revert {
				up = []string{insertOriginal}
				down = []string{"DELETE FROM " + table + " WHERE id = 1", insertDuplicate, insertDuplicate}
			}
			const name = "202611160001_atomic_dml_test"
			current, err := NewSQL(name, up, down)
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
			var expectedOriginal int64
			var failure error
			if revert {
				if _, err := runner.Apply(ctx); err != nil {
					t.Fatal(err)
				}
				if journal, err := store.Journal(ctx); err != nil || len(journal) != 1 || journal[0].State != JournalApplied {
					rows, queryErr := database.Query("SELECT failure_code FROM " + migrationJournalTable)
					t.Fatalf("成功迁移日志不可读取: %#v %v，数据库返回 failure_code=%#v %v", journal, err, rows, queryErr)
				}
				_, failure = runner.Rollback(ctx, 1)
				expectedOriginal = 1
			} else {
				_, failure = runner.Apply(ctx)
			}
			if failure == nil || !strings.Contains(failure.Error(), duplicateKeyCode) {
				t.Fatalf("必须实际执行到数据库返回主键冲突: %v", failure)
			}
			if count, err := database.Table(table).Where("id = ?", int64(1)).Count(); err != nil || count != expectedOriginal {
				t.Fatalf("事务失败后原记录数错误: %d %v", count, err)
			}
			if count, err := database.Table(table).Where("id = ?", int64(2)).Count(); err != nil || count != 0 {
				t.Fatalf("已失败的 Down 不得留下新记录: %d %v", count, err)
			}
			if history, err := store.Applied(ctx); err != nil || int64(len(history)) != expectedOriginal {
				t.Fatalf("业务事务失败不应改写成功历史: %#v %v", history, err)
			}
			if journal, err := store.Journal(ctx); err != nil || len(journal) != 1 || journal[0].Name != name || journal[0].State != JournalFailed {
				t.Fatalf("失败迁移缺少持久日志: %#v %v", journal, err)
			}
		})
	}
}
