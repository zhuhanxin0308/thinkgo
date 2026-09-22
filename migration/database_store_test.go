//go:build cgo

package migration

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/db/builder"

	_ "github.com/mattn/go-sqlite3" // 测试真实事务、DDL 和迁移历史的一致性。
)

func newSQLiteMigrationDatabase(t *testing.T) *db.DB {
	t.Helper()
	return newSQLiteMigrationDatabaseAt(t, filepath.Join(t.TempDir(), "migration.sqlite"))
}

func newSQLiteMigrationDatabaseAt(t *testing.T, path string) *db.DB {
	t.Helper()
	handle, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		t.Fatalf("打开迁移测试数据库失败: %v", err)
	}
	handle.SetMaxOpenConns(1)
	database := db.NewDB(db.NewSQLConnection(handle, &builder.Sqlite{}))
	t.Cleanup(func() {
		if closeErr := database.Close(); closeErr != nil {
			t.Errorf("关闭迁移测试数据库失败: %v", closeErr)
		}
	})
	return database
}

// TestDatabaseStoreAppliesAndRevertsSQLiteMigration 验证真实事务会同时提交或回滚业务 DDL 与迁移历史。
func TestDatabaseStoreAppliesAndRevertsSQLiteMigration(t *testing.T) {
	database := newSQLiteMigrationDatabase(t)
	current, err := NewSQL(
		"202608150001_create_users",
		[]string{"CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)"},
		[]string{"DROP TABLE users"},
	)
	if err != nil {
		t.Fatalf("创建 SQL 迁移失败: %v", err)
	}
	registry := NewRegistry()
	if err := registry.Register(current); err != nil {
		t.Fatalf("注册 SQL 迁移失败: %v", err)
	}
	store, err := NewDatabaseStore(database)
	if err != nil {
		t.Fatalf("创建数据库迁移存储失败: %v", err)
	}
	runner, err := NewRunner(registry, store)
	if err != nil {
		t.Fatalf("创建数据库迁移运行器失败: %v", err)
	}
	result, err := runner.Apply(context.Background())
	if err != nil || len(result.Names) != 1 {
		t.Fatalf("应用数据库迁移失败: result=%#v err=%v", result, err)
	}
	if rows, queryErr := database.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", "users"); queryErr != nil || len(rows) != 1 {
		t.Fatalf("迁移没有创建业务表: rows=%v err=%v", rows, queryErr)
	}
	history, err := store.Applied(context.Background())
	if err != nil || len(history) != 1 || history[0].Checksum != current.Checksum() {
		t.Fatalf("迁移历史不完整: history=%#v err=%v", history, err)
	}

	rollback, err := runner.Rollback(context.Background(), 1)
	if err != nil || len(rollback.Names) != 1 {
		t.Fatalf("回滚数据库迁移失败: result=%#v err=%v", rollback, err)
	}
	if rows, queryErr := database.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", "users"); queryErr != nil || len(rows) != 0 {
		t.Fatalf("迁移回滚没有删除业务表: rows=%v err=%v", rows, queryErr)
	}
	if history, err = store.Applied(context.Background()); err != nil || len(history) != 0 {
		t.Fatalf("迁移回滚没有删除历史: history=%#v err=%v", history, err)
	}
}

// TestDatabaseStoreRollsBackFailedMigration 验证 Up 失败时业务变更和迁移历史都不会提交。
func TestDatabaseStoreRollsBackFailedMigration(t *testing.T) {
	database := newSQLiteMigrationDatabase(t)
	current, err := NewSQL(
		"202608150001_fail_after_create",
		[]string{
			"CREATE TABLE transient_users (id INTEGER PRIMARY KEY)",
			"CREATE TABLE transient_users (id INTEGER PRIMARY KEY)",
		},
		[]string{"DROP TABLE transient_users"},
	)
	if err != nil {
		t.Fatalf("创建失败路径迁移失败: %v", err)
	}
	store, err := NewDatabaseStore(database)
	if err != nil {
		t.Fatalf("创建数据库迁移存储失败: %v", err)
	}
	if err = store.Ensure(context.Background()); err != nil {
		t.Fatalf("初始化迁移历史失败: %v", err)
	}
	if err = store.WithMigrationLock(context.Background(), func(locked Store) error {
		return locked.Apply(context.Background(), current, 1)
	}); err == nil {
		t.Fatal("重复建表必须使迁移失败")
	}
	rows, queryErr := database.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", "transient_users")
	if queryErr != nil || len(rows) != 0 {
		t.Fatalf("失败迁移的 DDL 必须回滚: rows=%v err=%v", rows, queryErr)
	}
	history, historyErr := store.Applied(context.Background())
	if historyErr != nil || len(history) != 0 {
		t.Fatalf("失败迁移不应写入历史: history=%#v err=%v", history, historyErr)
	}
	if !errors.Is(current.Down(context.Background(), nil), ErrInvalidMigration) {
		t.Fatal("SQL 迁移在缺少执行器时必须返回稳定错误")
	}
}

// TestDatabaseStoreSerializesIndependentSQLiteRunners 验证两个进程等价的 Store/Runner 通过数据库锁只执行一次 DDL。
func TestDatabaseStoreSerializesIndependentSQLiteRunners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent-migration.sqlite")
	databases := []*db.DB{newSQLiteMigrationDatabaseAt(t, path), newSQLiteMigrationDatabaseAt(t, path)}
	var upCalls atomic.Int64
	current, err := New(
		"202608150001_create_once",
		firstChecksum,
		func(ctx context.Context, executor Executor) error {
			upCalls.Add(1)
			_, executeErr := executor.ExecuteContext(ctx, "CREATE TABLE execute_once (id INTEGER PRIMARY KEY)")
			return executeErr
		},
		func(ctx context.Context, executor Executor) error {
			_, executeErr := executor.ExecuteContext(ctx, "DROP TABLE execute_once")
			return executeErr
		},
	)
	if err != nil {
		t.Fatalf("创建并发 SQLite 迁移失败: %v", err)
	}
	runners := make([]*Runner, 0, len(databases))
	for _, database := range databases {
		registry := NewRegistry()
		if err = registry.Register(current); err != nil {
			t.Fatalf("注册并发 SQLite 迁移失败: %v", err)
		}
		store, storeErr := NewDatabaseStore(database)
		if storeErr != nil {
			t.Fatalf("创建并发 SQLite Store 失败: %v", storeErr)
		}
		runner, runnerErr := NewRunner(registry, store)
		if runnerErr != nil {
			t.Fatalf("创建并发 SQLite Runner 失败: %v", runnerErr)
		}
		runners = append(runners, runner)
	}
	start := make(chan struct{})
	results := make(chan Result, len(runners))
	errorsChannel := make(chan error, len(runners))
	var wait sync.WaitGroup
	for _, runner := range runners {
		wait.Add(1)
		go func(currentRunner *Runner) {
			defer wait.Done()
			<-start
			result, applyErr := currentRunner.Apply(context.Background())
			results <- result
			errorsChannel <- applyErr
		}(runner)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	for applyErr := range errorsChannel {
		if applyErr != nil {
			t.Fatalf("并发 SQLite 迁移失败: %v", applyErr)
		}
	}
	applied := 0
	for result := range results {
		if len(result.Names) == 1 {
			applied++
		}
	}
	if applied != 1 || upCalls.Load() != 1 {
		t.Fatalf("SQLite 迁移没有全局串行化: applied=%d up_calls=%d", applied, upCalls.Load())
	}
}

// TestDatabaseStorePersistsFailureAndRequiresExplicitRecovery 验证 DDL/历史不一致不会静默 clean。
func TestDatabaseStorePersistsFailureAndRequiresExplicitRecovery(t *testing.T) {
	database := newSQLiteMigrationDatabase(t)
	store, err := NewDatabaseStore(database)
	if err != nil {
		t.Fatalf("创建 dirty 恢复 Store 失败: %v", err)
	}
	if err = store.Ensure(context.Background()); err != nil {
		t.Fatalf("初始化 dirty 恢复表失败: %v", err)
	}
	if _, err = database.ExecuteContext(
		context.Background(),
		"CREATE TRIGGER fail_migration_history BEFORE INSERT ON thinkgo_migrations BEGIN SELECT RAISE(ABORT, 'history failed'); END",
	); err != nil {
		t.Fatalf("创建历史失败触发器失败: %v", err)
	}
	current, err := NewSQL(
		"202608150001_create_recoverable",
		[]string{"CREATE TABLE recoverable_rows (id INTEGER PRIMARY KEY)"},
		[]string{"DROP TABLE recoverable_rows"},
	)
	if err != nil {
		t.Fatalf("创建 dirty 恢复迁移失败: %v", err)
	}
	registry := NewRegistry()
	if err = registry.Register(current); err != nil {
		t.Fatalf("注册 dirty 恢复迁移失败: %v", err)
	}
	runner, err := NewRunner(registry, store)
	if err != nil {
		t.Fatalf("创建 dirty 恢复 Runner 失败: %v", err)
	}
	if _, err = runner.Apply(context.Background()); err == nil {
		t.Fatal("迁移历史写入失败必须使 Apply 失败")
	}
	if rows, queryErr := database.QueryContext(context.Background(), "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", "recoverable_rows"); queryErr != nil || len(rows) != 0 {
		t.Fatalf("SQLite savepoint 没有回滚 DDL: rows=%#v err=%v", rows, queryErr)
	}
	journal, err := store.Journal(context.Background())
	if err != nil || len(journal) != 1 || journal[0].State != JournalFailed {
		t.Fatalf("失败迁移没有 durable failed journal: journal=%#v err=%v", journal, err)
	}
	if _, err = runner.Apply(context.Background()); !errors.Is(err, ErrMigrationDirty) {
		t.Fatalf("dirty journal 没有阻止自动重试: %v", err)
	}
	if err = runner.ResolveDirty(context.Background(), current.Name(), RecoveryMarkReverted); err != nil {
		t.Fatalf("显式确认 schema 未应用失败: %v", err)
	}
	if _, err = database.ExecuteContext(context.Background(), "DROP TRIGGER fail_migration_history"); err != nil {
		t.Fatalf("删除历史失败触发器失败: %v", err)
	}
	result, err := runner.Apply(context.Background())
	if err != nil || len(result.Names) != 1 {
		t.Fatalf("显式恢复后重新迁移失败: result=%#v err=%v", result, err)
	}
	journal, err = store.Journal(context.Background())
	if err != nil || len(journal) != 1 || journal[0].State != JournalApplied || journal[0].FencingToken <= 1 {
		t.Fatalf("恢复后 journal 最终状态错误: journal=%#v err=%v", journal, err)
	}
}

// TestDatabaseStoreFencingRejectsStaleJournalOwner 验证旧 token 不能覆盖新 owner 的 journal。
func TestDatabaseStoreFencingRejectsStaleJournalOwner(t *testing.T) {
	database := newSQLiteMigrationDatabase(t)
	store, err := NewDatabaseStore(database)
	if err != nil {
		t.Fatalf("创建 fencing Store 失败: %v", err)
	}
	current := mustMigration(t, "202608150001_fenced", firstChecksum)
	err = store.WithMigrationLock(context.Background(), func(locked Store) error {
		bound := locked.(*DatabaseStore)
		if recordErr := bound.recordApplying(context.Background(), current, 1, JournalOperationApply); recordErr != nil {
			return recordErr
		}
		if _, updateErr := bound.activeExecutor().ExecuteContext(
			context.Background(),
			"UPDATE "+migrationFenceTable+" SET fencing_token = ? WHERE lock_name = ?",
			bound.fencingToken+1,
			migrationFenceLockName,
		); updateErr != nil {
			return updateErr
		}
		if failedErr := bound.recordFailed(context.Background(), current.Name(), migrationFailureOperation); !errors.Is(failedErr, ErrMigrationLockLost) {
			t.Fatalf("旧 fencing owner 覆盖了新 journal: %v", failedErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("执行 fencing 覆盖测试失败: %v", err)
	}
}

// TestDatabaseStoreMarksVerifiedDirtyMigrationApplied 验证人工确认 DDL 已存在时可原子补齐历史和 journal。
func TestDatabaseStoreMarksVerifiedDirtyMigrationApplied(t *testing.T) {
	database := newSQLiteMigrationDatabase(t)
	store, err := NewDatabaseStore(database)
	if err != nil {
		t.Fatalf("创建 mark-applied Store 失败: %v", err)
	}
	if err = store.Ensure(context.Background()); err != nil {
		t.Fatalf("初始化 mark-applied 表失败: %v", err)
	}
	if _, err = database.ExecuteContext(
		context.Background(),
		"INSERT INTO "+migrationFenceTable+" (lock_name, fencing_token) VALUES (?, ?)",
		migrationFenceLockName,
		int64(1),
	); err != nil {
		t.Fatalf("写入恢复 fencing token 失败: %v", err)
	}
	name := "202608150001_verified_schema"
	if _, err = database.ExecuteContext(
		context.Background(),
		"INSERT INTO "+migrationJournalTable+" (name, checksum, batch, operation, state, fencing_token, started_at, finished_at, failure_code) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		name, firstChecksum, int64(1), string(JournalOperationApply), string(JournalFailed), int64(1), time.Now().UTC(), time.Now().UTC(), migrationFailureOperation,
	); err != nil {
		t.Fatalf("写入待恢复 journal 失败: %v", err)
	}
	registry := NewRegistry()
	if err = registry.Register(mustMigration(t, name, firstChecksum)); err != nil {
		t.Fatalf("注册待恢复迁移失败: %v", err)
	}
	runner, err := NewRunner(registry, store)
	if err != nil {
		t.Fatalf("创建待恢复 Runner 失败: %v", err)
	}
	if err = runner.ResolveDirty(context.Background(), name, RecoveryMarkApplied); err != nil {
		t.Fatalf("确认 dirty 迁移已应用失败: %v", err)
	}
	history, err := store.Applied(context.Background())
	if err != nil || len(history) != 1 || history[0].Name != name {
		t.Fatalf("恢复后历史不完整: history=%#v err=%v", history, err)
	}
	journal, err := store.Journal(context.Background())
	if err != nil || len(journal) != 1 || journal[0].State != JournalApplied || journal[0].FencingToken != 2 {
		t.Fatalf("恢复后 journal 不完整: journal=%#v err=%v", journal, err)
	}
}

// TestDatabaseStoreInspectStatusRemainsReadOnly 验证预检在未初始化时不建表，初始化后读取 journal 一致状态。
func TestDatabaseStoreInspectStatusRemainsReadOnly(t *testing.T) {
	database := newSQLiteMigrationDatabase(t)
	store, err := NewDatabaseStore(database)
	if err != nil {
		t.Fatalf("创建只读预检 Store 失败: %v", err)
	}
	registry := NewRegistry()
	current := mustMigration(t, "202608150001_inspect", firstChecksum)
	if err = registry.Register(current); err != nil {
		t.Fatalf("注册只读预检迁移失败: %v", err)
	}
	runner, err := NewRunner(registry, store)
	if err != nil {
		t.Fatalf("创建只读预检 Runner 失败: %v", err)
	}
	statuses, initialized, err := runner.InspectStatus(context.Background())
	if err != nil || initialized || len(statuses) != 1 || statuses[0].Applied {
		t.Fatalf("未初始化预检结果错误: statuses=%#v initialized=%t err=%v", statuses, initialized, err)
	}
	if rows, queryErr := database.QueryContext(context.Background(), "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", migrationHistoryTable); queryErr != nil || len(rows) != 0 {
		t.Fatalf("只读预检创建了迁移表: rows=%#v err=%v", rows, queryErr)
	}
	if _, err = runner.Apply(context.Background()); err != nil {
		t.Fatalf("应用预检迁移失败: %v", err)
	}
	statuses, initialized, err = runner.InspectStatus(context.Background())
	if err != nil || !initialized || len(statuses) != 1 || !statuses[0].Applied {
		t.Fatalf("初始化后预检结果错误: statuses=%#v initialized=%t err=%v", statuses, initialized, err)
	}
}

// TestDatabaseStoreRecoveryReconcilesExistingHistory 验证恢复操作会校验并保留或删除已经存在的匹配历史。
func TestDatabaseStoreRecoveryReconcilesExistingHistory(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		operation  JournalOperation
		resolution RecoveryResolution
		wantState  JournalState
		wantRows   int
	}{
		{name: "keep_verified_history", operation: JournalOperationApply, resolution: RecoveryMarkApplied, wantState: JournalApplied, wantRows: 1},
		{name: "delete_reverted_history", operation: JournalOperationRevert, resolution: RecoveryMarkReverted, wantState: JournalReverted, wantRows: 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			database := newSQLiteMigrationDatabase(t)
			store, err := NewDatabaseStore(database)
			if err != nil {
				t.Fatalf("创建历史恢复 Store 失败: %v", err)
			}
			if err = store.Ensure(context.Background()); err != nil {
				t.Fatalf("初始化历史恢复表失败: %v", err)
			}
			name := "202608150001_existing_history"
			if _, err = database.ExecuteContext(
				context.Background(),
				"INSERT INTO "+migrationFenceTable+" (lock_name, fencing_token) VALUES (?, ?)",
				migrationFenceLockName, int64(3),
			); err != nil {
				t.Fatalf("写入历史恢复 fence 失败: %v", err)
			}
			if _, err = database.ExecuteContext(
				context.Background(),
				"INSERT INTO "+migrationHistoryTable+" (name, checksum, batch, applied_at) VALUES (?, ?, ?, ?)",
				name, firstChecksum, int64(1), time.Now().UTC(),
			); err != nil {
				t.Fatalf("写入待核验历史失败: %v", err)
			}
			if _, err = database.ExecuteContext(
				context.Background(),
				"INSERT INTO "+migrationJournalTable+" (name, checksum, batch, operation, state, fencing_token, started_at, finished_at, failure_code) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
				name, firstChecksum, int64(1), string(testCase.operation), string(JournalFailed), int64(3), time.Now().UTC(), time.Now().UTC(), migrationFailureOperation,
			); err != nil {
				t.Fatalf("写入历史恢复 journal 失败: %v", err)
			}
			registry := NewRegistry()
			if err = registry.Register(mustMigration(t, name, firstChecksum)); err != nil {
				t.Fatalf("注册历史恢复迁移失败: %v", err)
			}
			runner, runnerErr := NewRunner(registry, store)
			if runnerErr != nil {
				t.Fatalf("创建历史恢复 Runner 失败: %v", runnerErr)
			}
			if err = runner.ResolveDirty(context.Background(), name, testCase.resolution); err != nil {
				t.Fatalf("核验已有历史失败: %v", err)
			}
			history, historyErr := store.Applied(context.Background())
			if historyErr != nil || len(history) != testCase.wantRows {
				t.Fatalf("历史恢复结果错误: history=%#v err=%v", history, historyErr)
			}
			journal, journalErr := store.Journal(context.Background())
			if journalErr != nil || len(journal) != 1 || journal[0].State != testCase.wantState || journal[0].FencingToken != 4 {
				t.Fatalf("历史恢复 journal 错误: journal=%#v err=%v", journal, journalErr)
			}
		})
	}
}

// TestDatabaseStoreLockPanicRollsBackAndRethrows 验证 panic 路径回滚 SQLite 外层锁事务并保持原 panic。
func TestDatabaseStoreLockPanicRollsBackAndRethrows(t *testing.T) {
	database := newSQLiteMigrationDatabase(t)
	store, err := NewDatabaseStore(database)
	if err != nil {
		t.Fatalf("创建 panic 清理 Store 失败: %v", err)
	}
	panicValue := "migration callback panic"
	func() {
		defer func() {
			if recovered := recover(); recovered != panicValue {
				t.Fatalf("迁移锁没有保持原 panic: %#v", recovered)
			}
		}()
		_ = store.WithMigrationLock(context.Background(), func(Store) error {
			panic(panicValue)
		})
	}()
	if rows, queryErr := database.QueryContext(
		context.Background(),
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?",
		migrationHistoryTable,
	); queryErr != nil || len(rows) != 0 {
		t.Fatalf("panic 后 SQLite 外层事务没有回滚: rows=%#v err=%v", rows, queryErr)
	}
	if _, executeErr := database.ExecuteContext(context.Background(), "CREATE TABLE panic_recovery_probe (id INTEGER PRIMARY KEY)"); executeErr != nil {
		t.Fatalf("panic 丢弃连接后数据库不可继续使用: %v", executeErr)
	}
}

// TestDatabaseStoreSQLiteLockWaitHonorsContext 验证真实文件锁竞争会在上下文截止时取消等待。
func TestDatabaseStoreSQLiteLockWaitHonorsContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock-cancel.sqlite")
	blocker := newSQLiteMigrationDatabaseAt(t, path)
	contender := newSQLiteMigrationDatabaseAt(t, path)
	locked := make(chan struct{})
	release := make(chan struct{})
	blockerDone := make(chan error, 1)
	go func() {
		blockerDone <- blocker.WithPinnedSQLConnection(context.Background(), func(connection db.PinnedSQLConnection) error {
			if _, err := connection.ExecuteContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
				return err
			}
			close(locked)
			<-release
			_, err := connection.ExecuteContext(context.Background(), "ROLLBACK")
			return err
		})
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("阻塞连接没有取得 SQLite 写锁")
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	store, err := NewDatabaseStore(contender)
	if err != nil {
		t.Fatalf("创建锁取消 Store 失败: %v", err)
	}
	registry := NewRegistry()
	if err = registry.Register(mustMigration(t, "202608150001_lock_cancel", firstChecksum)); err != nil {
		t.Fatalf("注册锁取消迁移失败: %v", err)
	}
	runner, err := NewRunner(registry, store)
	if err != nil {
		t.Fatalf("创建锁取消 Runner 失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	if _, err = runner.Apply(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SQLite 锁等待没有传播截止错误: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("SQLite 锁等待没有及时响应上下文: %s", elapsed)
	}
	close(release)
	if err = <-blockerDone; err != nil {
		t.Fatalf("释放阻塞 SQLite 写锁失败: %v", err)
	}
}
