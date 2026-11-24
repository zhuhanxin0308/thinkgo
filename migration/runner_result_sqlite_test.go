//go:build cgo

package migration

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/mattn/go-sqlite3"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type decoratedSQLiteStore struct{ *DatabaseStore }

func (s decoratedSQLiteStore) WithMigrationLock(ctx context.Context, callback func(Store) error) error {
	return s.DatabaseStore.WithMigrationLock(ctx, func(bound Store) error {
		return callback(struct {
			Store
			JournalStore
		}{bound, bound.(JournalStore)})
	})
}

func TestSQLiteRunnerNestedFailurePreservesOuterResult(t *testing.T) {
	innerDB := newSQLiteMigrationAuthorizerDB(t, func(operation int, action, _ string) bool {
		return operation == sqlite3.SQLITE_TRANSACTION && action == "COMMIT"
	})
	inner, err := NewDatabaseStore(innerDB)
	if err != nil {
		t.Fatal(err)
	}
	outerDB, _, _ := newSQLiteMigrationFaultDatabase(t, "")
	outer, err := NewDatabaseStore(outerDB)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	first := mustMigration(t, "202611240001_first", firstChecksum)
	if err := registry.Register(first); err != nil {
		t.Fatal(err)
	}
	second, err := New("202611240002_inner", secondChecksum, func(ctx context.Context, _ Executor) error {
		// 下层物理锁使用相同业务上下文；它的 COMMIT 错误不能作废外层的首项成功。
		return inner.WithMigrationLock(ctx, func(Store) error { return nil })
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(second); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(registry, decoratedSQLiteStore{outer})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Apply(t.Context())
	if !errors.Is(err, ErrMigrationOutcomeUnknown) || result.Batch != 1 || !slices.Equal(result.Names, []string{first.Name()}) {
		t.Fatalf("嵌套事务结果覆盖了外层真实持久结果：%+v %v", result, err)
	}
	history, err := outer.Applied(t.Context())
	if err != nil || len(history) != 1 || history[0].Name != first.Name() {
		t.Fatalf("外层历史不符：%v %v", history, err)
	}
}

func TestSQLiteFinishedTransactionIgnoresRowsAffectedForOutcome(t *testing.T) {
	cause := sqlite3.Error{Code: sqlite3.ErrBusy}
	for _, statement := range []string{"COMMIT", "ROLLBACK"} {
		session := &migrationLockSessionProbe{dialect: "sqlite", errors: map[string]error{
			statement: errors.Join(db.ErrRowsAffectedUnknown, cause),
		}}
		locker := &sqliteMigrationLocker{session: session}
		commit := statement == "COMMIT"
		// 即使元数据原因是 BUSY，已经执行成功的事务终结语句也不能重放。
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := locker.Release(ctx, commit)
		if !errors.Is(err, db.ErrRowsAffectedUnknown) || errors.Is(err, ErrMigrationOutcomeUnknown) ||
			errors.Is(err, context.Canceled) || locker.committed != commit || !slices.Equal(session.statements, []string{statement}) {
			t.Fatalf("SQL 执行成功后事务结局或执行次数错误：%s %v %+v", statement, err, session.statements)
		}
	}
}

func TestSQLiteRunnerReportsDurableChanges(t *testing.T) {
	const first = "202611240001_first"
	const second = "202611240002_second"
	for _, revert := range []bool{false, true} {
		for _, fault := range []string{"savepoint", "rollback", "commit", "restore_timeout", "business"} {
			direction := "Apply"
			if revert {
				direction = "Rollback"
			}
			t.Run(direction+"/"+fault, func(t *testing.T) {
				var armed atomic.Bool
				var rejections atomic.Int32
				database := newSQLiteMigrationAuthorizerDB(t, func(op int, action, arg string) bool {
					if !armed.Load() {
						return false
					}
					deny := (fault == "savepoint" || fault == "rollback") && op == sqlite3.SQLITE_SAVEPOINT && action == "RELEASE" ||
						fault == "rollback" && op == sqlite3.SQLITE_TRANSACTION && action == "ROLLBACK" ||
						fault == "commit" && op == sqlite3.SQLITE_TRANSACTION && action == "COMMIT" ||
						fault == "restore_timeout" && op == sqlite3.SQLITE_PRAGMA && action == "busy_timeout" && arg != "" && arg != "0"
					if deny {
						rejections.Add(1)
					}
					return deny
				})
				if _, err := database.Execute("CREATE TABLE sample (name TEXT PRIMARY KEY)"); err != nil {
					t.Fatal(err)
				}
				store, err := NewDatabaseStore(database)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Ensure(t.Context()); err != nil {
					t.Fatal(err)
				}
				registry := NewRegistry()
				businessErr := errors.New("迁移业务失败")
				for _, name := range []string{first, second} {
					step := func(down bool, sql string) func(context.Context, Executor) error {
						return func(ctx context.Context, executor Executor) error {
							if _, err := executor.ExecuteContext(ctx, sql, name); err != nil {
								return err
							}
							if revert == down && ((!down && name == second) || (down && name == first)) {
								armed.Store(true)
								if fault == "business" {
									return businessErr
								}
							}
							return nil
						}
					}
					current, err := New(name, firstChecksum, step(false, "INSERT INTO sample VALUES (?)"), step(true, "DELETE FROM sample WHERE name = ?"))
					if err != nil {
						t.Fatal(err)
					}
					if err := registry.Register(current); err != nil {
						t.Fatal(err)
					}
				}
				runner, err := NewRunner(registry, decoratedSQLiteStore{store})
				if err != nil {
					t.Fatal(err)
				}
				result, err := runner.Apply(t.Context())
				if revert {
					if err != nil || result.Batch != 1 || !slices.Equal(result.Names, []string{first, second}) {
						t.Fatalf("准备迁移未完整提交：%+v %v", result, err)
					}
					result, err = runner.Rollback(t.Context(), 1)
				}
				armed.Store(false)
				if err == nil {
					t.Fatal("丢失收尾或业务错误")
				}
				unknown := fault == "commit" || fault == "rollback"
				if errors.Is(err, ErrMigrationOutcomeUnknown) != unknown {
					t.Errorf("事务结局分类错误：%v", err)
				}
				if fault != "business" {
					var sqliteErr sqlite3.Error
					if !errors.As(err, &sqliteErr) || sqliteErr.Code != sqlite3.ErrAuth {
						t.Fatalf("丢失底层 SQLite 错误：%v", err)
					}
				}
				var changed, remaining []string
				journalStates := map[string]JournalState{}
				wantRejections := int32(1)
				switch fault {
				case "restore_timeout":
					changed, remaining = []string{first, second}, []string{first, second}
					journalStates[first], journalStates[second] = JournalApplied, JournalApplied
					if revert {
						changed, remaining = []string{second, first}, nil
						journalStates[first], journalStates[second] = JournalReverted, JournalReverted
					}
				case "business":
					wantRejections = 0
					if !errors.Is(err, businessErr) {
						t.Fatalf("丢失业务错误：%v", err)
					}
					changed, remaining = []string{first}, []string{first}
					journalStates[first], journalStates[second] = JournalApplied, JournalFailed
					if revert {
						changed = []string{second}
						journalStates[first], journalStates[second] = JournalFailed, JournalReverted
					}
				default:
					if fault == "rollback" {
						wantRejections = 2
					}
					if revert {
						remaining = []string{first, second}
						journalStates[first], journalStates[second] = JournalApplied, JournalApplied
					}
				}
				if rejections.Load() != wantRejections {
					t.Fatalf("故障注入次数 %d，应为 %d：%v", rejections.Load(), wantRejections, err)
				}
				wantBatch := int64(0)
				if len(changed) != 0 {
					wantBatch = 1
				}
				if result.Batch != wantBatch || !slices.Equal(result.Names, changed) {
					t.Errorf("报告的持久变更不符：%+v，应为 %v，错误 %v", result, changed, err)
				}
				rows, err := database.Query("SELECT name FROM sample ORDER BY name")
				if err != nil {
					t.Fatal(err)
				}
				var actual []string
				for _, row := range rows {
					actual = append(actual, row["name"].(string))
				}
				history, err := store.Applied(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				var applied []string
				for _, entry := range history {
					applied = append(applied, entry.Name)
				}
				slices.Sort(applied)
				if !slices.Equal(applied, remaining) || !slices.Equal(actual, remaining) {
					t.Errorf("实际业务行或历史不符：rows=%v history=%v 应为 %v", actual, applied, remaining)
				}
				journal, err := store.Journal(t.Context())
				if err != nil || len(journal) != len(journalStates) {
					t.Fatalf("journal 数量不符：%v %v，应为 %v", journal, err, journalStates)
				}
				for _, entry := range journal {
					if entry.State != journalStates[entry.Name] {
						t.Errorf("journal 状态不符：%+v，应为 %v", entry, journalStates)
					}
				}
			})
		}
	}
}
