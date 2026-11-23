//go:build cgo

package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mattn/go-sqlite3"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

type migrationSQLiteFaultConnector struct {
	backend *sqlite3.SQLiteDriver
	path    string
}

func (connector migrationSQLiteFaultConnector) Connect(context.Context) (driver.Conn, error) {
	return connector.backend.Open(connector.path)
}

func (connector migrationSQLiteFaultConnector) Driver() driver.Driver { return connector.backend }

func newSQLiteMigrationFaultDatabase(t *testing.T, fault string) (*db.DB, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	var armed atomic.Bool
	var rejected atomic.Int32
	backend := &sqlite3.SQLiteDriver{ConnectHook: func(connection *sqlite3.SQLiteConn) error {
		connection.RegisterAuthorizer(func(operation int, action, _, _ string) int {
			if operation == sqlite3.SQLITE_SAVEPOINT && action == fault && armed.CompareAndSwap(true, false) {
				rejected.Add(1)
				return sqlite3.SQLITE_DENY
			}
			return sqlite3.SQLITE_OK
		})
		return nil
	}}
	handle := sql.OpenDB(migrationSQLiteFaultConnector{backend: backend, path: filepath.Join(t.TempDir(), "savepoint-fault.sqlite")})
	handle.SetMaxOpenConns(1)
	database := db.NewDB(db.NewSQLConnection(handle, &builder.Sqlite{}))
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("关闭故障测试数据库失败：%v", err)
		}
	})
	return database, &armed, &rejected
}

// 在真实引擎中仅拒绝一个保存点操作，整事务回滚和之后的连接复用仍然可用。
func TestSQLiteSavepointFailurePreventsOuterCommit(t *testing.T) {
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",panicnil=1")
	cases := []struct {
		name    string
		fault   string
		outcome string
		swallow bool
	}{
		{name: "rollback_business_error", fault: "ROLLBACK", outcome: "error"},
		{name: "rollback_caught_panic", fault: "ROLLBACK", outcome: "panic"},
		{name: "rollback_caught_nil_panic", fault: "ROLLBACK", outcome: "nil_panic", swallow: true},
		{name: "rollback_ignored_error", fault: "ROLLBACK", outcome: "error", swallow: true},
		{name: "release_after_business_error", fault: "RELEASE", outcome: "error"},
		{name: "release_after_caught_panic", fault: "RELEASE", outcome: "panic"},
		{name: "release_after_success", fault: "RELEASE", outcome: "success"},
		{name: "successful_rollback_keeps_failed_journal", outcome: "error"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			database, armed, rejected := newSQLiteMigrationFaultDatabase(t, testCase.fault)
			if _, err := database.Execute("CREATE TABLE protected_rows (id INTEGER PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			store, err := NewDatabaseStore(database)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Ensure(t.Context()); err != nil {
				t.Fatal(err)
			}
			marker := errors.New("业务迁移失败")
			current, err := New("202611230002_savepoint_failure", firstChecksum, func(ctx context.Context, executor Executor) error {
				if _, err := executor.ExecuteContext(ctx, "INSERT INTO protected_rows VALUES (?)", 1); err != nil {
					return err
				}
				armed.Store(true)
				switch testCase.outcome {
				case "panic":
					panic(marker)
				case "nil_panic":
					panic(nil)
				case "error":
					return marker
				default:
					return nil
				}
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var applyReturned bool
			var recovered interface{}
			err = store.WithMigrationLock(t.Context(), func(locked Store) error {
				operationErr := func() (resultErr error) {
					defer func() {
						if !applyReturned {
							recovered = recover()
							resultErr = marker
						}
					}()
					resultErr = locked.Apply(t.Context(), current, 1)
					applyReturned = true
					return resultErr
				}()
				if testCase.swallow {
					return nil
				}
				return operationErr
			})
			if err == nil {
				t.Error("保存点清理失败或业务错误被报告为成功")
			}
			if !testCase.swallow && testCase.outcome != "success" && !errors.Is(err, marker) {
				t.Errorf("原业务错误丢失：%v", err)
			}
			panicked := testCase.outcome == "panic" || testCase.outcome == "nil_panic"
			if applyReturned == panicked || testCase.outcome == "panic" && recovered != marker || testCase.outcome == "nil_panic" && recovered != nil {
				t.Errorf("保存点清理改变了业务退出方式：returned=%t panic=%v", applyReturned, recovered)
			}
			if testCase.fault != "" {
				var sqliteErr sqlite3.Error
				if rejected.Load() != 1 || !errors.As(err, &sqliteErr) || sqliteErr.Code != sqlite3.ErrAuth {
					t.Errorf("未保留唯一的真实保存点故障：拒绝次数=%d err=%v", rejected.Load(), err)
				} else if strings.Count(err.Error(), sqliteErr.Error()) != 1 {
					t.Errorf("一次保存点故障被重复返回：%v", err)
				}
			}
			if rows, queryErr := database.Query("SELECT id FROM protected_rows"); queryErr != nil || len(rows) != 0 {
				t.Errorf("不安全的保存点被外层提交：rows=%v err=%v", rows, queryErr)
			}
			entries, journalErr := store.Journal(t.Context())
			if journalErr != nil {
				t.Fatal(journalErr)
			}
			if testCase.fault == "" {
				if len(entries) != 1 || entries[0].State != JournalFailed {
					t.Errorf("成功回滚后的 failed journal 没有保留：%v", entries)
				}
			} else if len(entries) != 0 {
				t.Errorf("不确定的事务仍提交了迁移日志：%v", entries)
			}
			if history, historyErr := store.Applied(t.Context()); historyErr != nil || len(history) != 0 {
				t.Errorf("失败迁移仍提交了历史：%v %v", history, historyErr)
			}
			if err := store.WithMigrationLock(t.Context(), func(Store) error { return nil }); err != nil {
				t.Errorf("保存点故障后无法重新获得迁移锁：%v", err)
			}
			if _, err := database.Execute("INSERT INTO protected_rows VALUES (?)", 2); err != nil {
				t.Errorf("保存点故障后物理连接无法复用：%v", err)
			}
		})
	}
}

// Down 已删除业务行后保存点失败，必须恢复数据、history 和原 applied journal。
func TestSQLiteRevertSavepointFailurePreservesAppliedState(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		fault   string
		failure bool
	}{
		{name: "rollback_after_error", fault: "ROLLBACK", failure: true},
		{name: "release_after_error", fault: "RELEASE", failure: true},
		{name: "release_after_success", fault: "RELEASE"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			database, armed, rejected := newSQLiteMigrationFaultDatabase(t, testCase.fault)
			if _, err := database.Execute("CREATE TABLE protected_rows (id INTEGER PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			marker := errors.New("回退迁移失败")
			current, err := New("202611230003_revert_failure", firstChecksum,
				func(ctx context.Context, executor Executor) error {
					_, err := executor.ExecuteContext(ctx, "INSERT INTO protected_rows VALUES (?)", 1)
					return err
				}, func(ctx context.Context, executor Executor) error {
					if _, err := executor.ExecuteContext(ctx, "DELETE FROM protected_rows"); err != nil {
						return err
					}
					armed.Store(true)
					if testCase.failure {
						return marker
					}
					return nil
				})
			if err != nil {
				t.Fatal(err)
			}
			store, err := NewDatabaseStore(database)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.WithMigrationLock(t.Context(), func(locked Store) error { return locked.Apply(t.Context(), current, 1) }); err != nil {
				t.Fatal(err)
			}
			err = store.WithMigrationLock(t.Context(), func(locked Store) error { return locked.Revert(t.Context(), current) })
			var sqliteErr sqlite3.Error
			if rejected.Load() != 1 || !errors.As(err, &sqliteErr) || sqliteErr.Code != sqlite3.ErrAuth || testCase.failure && !errors.Is(err, marker) {
				t.Fatalf("回退迁移没有保留真实业务及保存点错误：%v", err)
			}
			if strings.Count(err.Error(), sqliteErr.Error()) != 1 {
				t.Errorf("回退迁移重复返回同一个保存点故障：%v", err)
			}
			if rows, queryErr := database.Query("SELECT id FROM protected_rows"); queryErr != nil || len(rows) != 1 {
				t.Errorf("失败的 Down 仍删除了业务数据：%v %v", rows, queryErr)
			}
			if history, historyErr := store.Applied(t.Context()); historyErr != nil || len(history) != 1 || history[0].Name != current.Name() {
				t.Errorf("失败的 Down 改变了原历史：%v %v", history, historyErr)
			}
			if entries, journalErr := store.Journal(t.Context()); journalErr != nil || len(entries) != 1 || entries[0].State != JournalApplied {
				t.Errorf("失败的 Down 改变了原 journal：%v %v", entries, journalErr)
			}
			if err := store.WithMigrationLock(t.Context(), func(Store) error { return nil }); err != nil {
				t.Errorf("回退故障污染了后续迁移锁：%v", err)
			}
		})
	}
}
