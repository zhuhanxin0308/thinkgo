package migration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

// TestDatabaseStoreReadOnlyStatePathsWithoutCGO 覆盖不依赖真实 SQLite 驱动的只读状态读取路径。
func TestDatabaseStoreReadOnlyStatePathsWithoutCGO(t *testing.T) {
	name := "202608150001_create_users"
	session := &migrationJournalSessionProbe{
		dialect: "postgres",
		queryResults: map[string][]map[string]interface{}{
			"information_schema.tables": {{"present": int64(1)}},
			"SELECT name, checksum, batch, applied_at FROM " + migrationHistoryTable: {{
				"name": name, "checksum": firstChecksum, "batch": int64(2), "applied_at": "2026-08-15T00:00:00Z",
			}},
			"SELECT name, checksum, batch, operation, state, fencing_token, started_at, finished_at, failure_code FROM " + migrationJournalTable: {{
				"name": name, "checksum": firstChecksum, "batch": int64(2),
				"operation": string(JournalOperationApply), "state": string(JournalApplied), "fencing_token": int64(2),
				"started_at": "2026-08-15T00:00:00Z", "finished_at": "2026-08-15T00:00:01Z", "failure_code": nil,
			}},
		},
	}
	store := newLockedJournalStore("postgres", session)
	ctx := context.Background()

	if err := store.Ensure(ctx); err != nil {
		t.Fatalf("已存在迁移表时 Ensure 失败: %v", err)
	}
	history, initialized, err := store.Inspect(ctx)
	if err != nil || !initialized || len(history) != 1 || history[0].Name != name {
		t.Fatalf("只读迁移快照错误: history=%#v initialized=%t err=%v", history, initialized, err)
	}
	entries, err := store.Journal(ctx)
	if err != nil || len(entries) != 1 || entries[0].State != JournalApplied {
		t.Fatalf("只读 journal 快照错误: entries=%#v err=%v", entries, err)
	}
	if applied, err := store.Applied(ctx); err != nil || len(applied) != 1 {
		t.Fatalf("Applied 读取错误: history=%#v err=%v", applied, err)
	}
}

// TestDatabaseStoreEnsureCreatesMissingTablesWithoutCGO 验证表不存在时会逐张创建三张治理表。
func TestDatabaseStoreEnsureCreatesMissingTablesWithoutCGO(t *testing.T) {
	session := &migrationJournalSessionProbe{dialect: "sqlite"}
	store := newLockedJournalStore("sqlite", session)
	if err := store.Ensure(context.Background()); err != nil {
		t.Fatalf("缺失迁移表创建失败: %v", err)
	}
	joined := strings.Join(session.statements, "\n")
	for _, table := range []string{migrationHistoryTable, migrationJournalTable, migrationFenceTable} {
		if !strings.Contains(joined, "CREATE TABLE "+table) {
			t.Fatalf("Ensure 未创建 %s: %s", table, joined)
		}
	}
}

// TestDatabaseStoreRecoveryStateTransitionsWithoutCGO 验证 applying/failed journal 的显式恢复只修改可证明的状态。
func TestDatabaseStoreRecoveryStateTransitionsWithoutCGO(t *testing.T) {
	name := "202608150001_create_users"
	newSession := func(history []map[string]interface{}) *migrationJournalSessionProbe {
		return &migrationJournalSessionProbe{
			dialect: "postgres",
			queryResults: map[string][]map[string]interface{}{
				"FROM " + migrationJournalTable + " WHERE name": {{
					"name": name, "checksum": firstChecksum, "batch": int64(3),
					"operation": string(JournalOperationApply), "state": string(JournalFailed), "fencing_token": int64(2),
					"started_at": "2026-08-15T00:00:00Z", "finished_at": "2026-08-15T00:00:01Z", "failure_code": "operation_failed",
				}},
				"SELECT checksum, batch FROM " + migrationHistoryTable + " WHERE name": history,
			},
		}
	}

	applySession := newSession(nil)
	applyStore := newLockedJournalStore("postgres", applySession)
	if err := applyStore.ResolveDirty(context.Background(), name, RecoveryMarkApplied); err != nil {
		t.Fatalf("恢复为 applied 失败: %v", err)
	}
	if joined := strings.Join(applySession.statements, "\n"); !strings.Contains(joined, "INSERT INTO "+migrationHistoryTable) || !strings.Contains(joined, "UPDATE "+migrationJournalTable) {
		t.Fatalf("恢复为 applied 未完成 history/journal 状态迁移: %s", joined)
	}

	revertSession := newSession([]map[string]interface{}{{"checksum": firstChecksum, "batch": int64(3)}})
	revertStore := newLockedJournalStore("postgres", revertSession)
	if err := revertStore.ResolveDirty(context.Background(), name, RecoveryMarkReverted); err != nil {
		t.Fatalf("恢复为 reverted 失败: %v", err)
	}
	if joined := strings.Join(revertSession.statements, "\n"); !strings.Contains(joined, "DELETE FROM "+migrationHistoryTable) || !strings.Contains(joined, "UPDATE "+migrationJournalTable) {
		t.Fatalf("恢复为 reverted 未完成 history/journal 状态迁移: %s", joined)
	}

	invalidSession := newSession(nil)
	invalidStore := newLockedJournalStore("postgres", invalidSession)
	if err := invalidStore.ResolveDirty(context.Background(), name, RecoveryResolution("unknown")); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("未知恢复结论没有被拒绝: %v", err)
	}
}

// TestDatabaseStoreRejectsCorruptReadOnlyRowsWithoutCGO 验证只读解析不会把损坏字段当成有效状态。
func TestDatabaseStoreRejectsCorruptReadOnlyRowsWithoutCGO(t *testing.T) {
	session := &migrationJournalSessionProbe{
		dialect: "postgres",
		queryResults: map[string][]map[string]interface{}{
			"information_schema.tables": {{"present": int64(1)}},
			"SELECT name, checksum, batch, applied_at FROM " + migrationHistoryTable: {{"name": "bad", "checksum": firstChecksum, "batch": "not-an-int", "applied_at": "now"}},
		},
	}
	store := newLockedJournalStore("postgres", session)
	if _, err := store.Applied(context.Background()); !errors.Is(err, ErrMigrationDrift) {
		t.Fatalf("损坏迁移历史没有被拒绝: %v", err)
	}
	if _, initialized, err := store.Inspect(context.Background()); !initialized || !errors.Is(err, ErrMigrationDrift) {
		t.Fatalf("损坏迁移历史的 Inspect 结果错误: initialized=%t err=%v", initialized, err)
	}
}

// TestDatabaseStoreSQLiteSavepointStateWithoutCGO 验证 SQLite 锁内的保存点成功和失败收尾路径。
func TestDatabaseStoreSQLiteSavepointStateWithoutCGO(t *testing.T) {
	session := &migrationJournalSessionProbe{dialect: "sqlite"}
	store := newLockedJournalStore("sqlite", session)
	store.sqliteLocked = true
	if err := store.runAtomic(context.Background(), func(Executor) error { return nil }); err != nil {
		t.Fatalf("保存点成功路径失败: %v", err)
	}
	if err := store.runAtomic(context.Background(), func(Executor) error { return errors.New("step failed") }); err == nil || !strings.Contains(err.Error(), "step failed") {
		t.Fatalf("保存点失败路径没有保留主错误: %v", err)
	}
	joined := strings.Join(session.statements, "\n")
	if !strings.Contains(joined, "SAVEPOINT "+sqliteMigrationSavepoint) || !strings.Contains(joined, "ROLLBACK TO SAVEPOINT "+sqliteMigrationSavepoint) {
		t.Fatalf("保存点没有执行完整收尾: %s", joined)
	}
}

// TestDatabaseStoreReadLockSessionStateWithoutCGO 验证只读锁会话也执行方言锁并正确释放。
func TestDatabaseStoreReadLockSessionStateWithoutCGO(t *testing.T) {
	session := &migrationLockSessionProbe{dialect: "postgres", results: map[string]interface{}{"pg_advisory_lock": nil, "pg_advisory_unlock": true}}
	store := &DatabaseStore{database: db.NewDB(nil), dialect: "postgres"}
	called := false
	if err := store.withMigrationReadSession(context.Background(), session, func(Store) error {
		called = true
		return nil
	}); err != nil || !called {
		t.Fatalf("只读锁会话执行失败: called=%t err=%v", called, err)
	}
}

// TestDatabaseStoreFencingAndSQLiteBranchesWithoutCGO 覆盖 fencing 初始化以及 SQLite 事务性迁移分支。
func TestDatabaseStoreFencingAndSQLiteBranchesWithoutCGO(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres", "sqlite", "sqlserver", "oracle"} {
		if _, _, err := migrationTableStatements(dialect); err != nil {
			t.Fatalf("方言 %s 的迁移表语句生成失败: %v", dialect, err)
		}
	}
	if _, _, err := migrationTableStatements("unknown"); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("未知方言没有被拒绝: %v", err)
	}

	fenceSession := &migrationJournalSessionProbe{
		dialect:      "postgres",
		zeroFragment: "UPDATE " + migrationFenceTable,
		queryResults: map[string][]map[string]interface{}{
			"SELECT fencing_token": {{"fencing_token": int64(9)}},
		},
	}
	store := newLockedJournalStore("postgres", fenceSession)
	token, err := store.nextFencingTokenWithExecutor(context.Background(), fenceSession)
	if err != nil || token != 9 {
		t.Fatalf("fencing token 初始化分支错误: token=%d err=%v", token, err)
	}

	name := "202608150001_create_users"
	recoverySession := &migrationJournalSessionProbe{
		dialect: "postgres",
		queryResults: map[string][]map[string]interface{}{
			"FROM " + migrationJournalTable + " WHERE name": {{
				"name": name, "checksum": firstChecksum, "batch": int64(3),
				"operation": string(JournalOperationApply), "state": string(JournalFailed), "fencing_token": int64(2),
				"started_at": "2026-08-15T00:00:00Z", "finished_at": "2026-08-15T00:00:01Z", "failure_code": "operation_failed",
			}},
			"SELECT checksum, batch FROM " + migrationHistoryTable + " WHERE name": {{"checksum": firstChecksum, "batch": int64(3)}},
		},
	}
	recoveryStore := newLockedJournalStore("postgres", recoverySession)
	if err := recoveryStore.ResolveDirty(context.Background(), name, RecoveryMarkApplied); err != nil {
		t.Fatalf("已有一致 history 时恢复为 applied 失败: %v", err)
	}

	current, err := NewSQL(name, []string{"CREATE TABLE users (id INTEGER)"}, []string{"DROP TABLE users"})
	if err != nil {
		t.Fatalf("创建 SQLite 分支迁移失败: %v", err)
	}
	for _, operation := range []struct {
		name string
		run  func(*DatabaseStore, context.Context, Migration) error
	}{{"apply", func(s *DatabaseStore, ctx context.Context, m Migration) error { return s.applyMigration(ctx, m, 1) }}, {"revert", func(s *DatabaseStore, ctx context.Context, m Migration) error { return s.revertMigration(ctx, m) }}} {
		t.Run(operation.name, func(t *testing.T) {
			session := &migrationJournalSessionProbe{dialect: "sqlite"}
			branchStore := newLockedJournalStore("sqlite", session)
			branchStore.sqliteLocked = true
			if err := operation.run(branchStore, context.Background(), current); err != nil {
				t.Fatalf("SQLite %s 分支失败: %v", operation.name, err)
			}
		})
	}
}

// TestDatabaseStoreReadLockInputValidationWithoutCGO 验证只读锁入口在依赖、上下文和回调非法时快速失败。
func TestDatabaseStoreReadLockInputValidationWithoutCGO(t *testing.T) {
	callback := func(Store) error { return nil }
	var nilContext context.Context
	if err := (*DatabaseStore)(nil).WithMigrationReadLock(context.Background(), callback); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("空 Store 只读锁错误不稳定: %v", err)
	}
	store := &DatabaseStore{database: db.NewDB(nil), dialect: "postgres"}
	if err := store.WithMigrationReadLock(nilContext, callback); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("空上下文只读锁错误不稳定: %v", err)
	}
	if err := store.WithMigrationReadLock(context.Background(), nil); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("空回调只读锁错误不稳定: %v", err)
	}
}

// TestMigrationLockAndSQLHelpersWithoutCGO 覆盖锁结果、SQL 迁移执行器和错误包装的边界。
func TestMigrationLockAndSQLHelpersWithoutCGO(t *testing.T) {
	if value, err := migrationLockInt([]map[string]interface{}{{"value": int64(3)}}, "value"); err != nil || value != 3 {
		t.Fatalf("锁整数解析失败: value=%d err=%v", value, err)
	}
	if value, err := migrationOnlyInt([]map[string]interface{}{{"value": int64(4)}}); err != nil || value != 4 {
		t.Fatalf("单列整数解析失败: value=%d err=%v", value, err)
	}
	if value, err := migrationLockText([]map[string]interface{}{{"scope": []byte("db")}}, "scope"); err != nil || value != "db" {
		t.Fatalf("锁文本解析失败: value=%q err=%v", value, err)
	}
	if !isSQLiteBusyOrLocked(errors.New("database is locked")) || isSQLiteBusyOrLocked(errors.New("unrelated")) {
		t.Fatal("SQLite 锁错误识别错误")
	}

	session := &migrationJournalSessionProbe{dialect: "postgres"}
	current, err := NewSQL("202608150001_helper", []string{" SELECT 1 ", "SELECT 2"}, nil)
	if err != nil {
		t.Fatalf("创建辅助 SQL 迁移失败: %v", err)
	}
	if err := current.Up(context.Background(), session); err != nil || len(session.statements) != 2 {
		t.Fatalf("SQL 迁移 Up 执行错误: statements=%v err=%v", session.statements, err)
	}
	if err := current.Down(context.Background(), session); !errors.Is(err, ErrIrreversibleMigration) {
		t.Fatalf("不可逆 SQL 迁移没有返回明确错误: %v", err)
	}
	var nilContext context.Context
	if err := current.Up(nilContext, session); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("空上下文 SQL 迁移没有被拒绝: %v", err)
	}
	if _, err := NewSQL("202608150001_helper", []string{"\x00"}, nil); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("包含空字节的 SQL 没有被拒绝: %v", err)
	}
}

// TestDatabaseStoreWriteValidationAndJournalTransitionsWithoutCGO 覆盖写入口参数与 journal 状态迁移边界。
func TestDatabaseStoreWriteValidationAndJournalTransitionsWithoutCGO(t *testing.T) {
	current := mustMigration(t, "202608150001_validation", firstChecksum)
	store := &DatabaseStore{database: db.NewDB(nil), dialect: "postgres"}
	var nilContext context.Context
	for _, testCase := range []struct {
		name string
		err  error
	}{
		{"nil_store_apply", (*DatabaseStore)(nil).Apply(context.Background(), current, 1)},
		{"nil_context_apply", store.Apply(nilContext, current, 1)},
		{"zero_batch_apply", store.Apply(context.Background(), current, 0)},
		{"nil_store_revert", (*DatabaseStore)(nil).Revert(context.Background(), current)},
		{"nil_context_revert", store.Revert(nilContext, current)},
		{"nil_migration_revert", store.Revert(context.Background(), nil)},
	} {
		if !errors.Is(testCase.err, ErrInvalidMigration) {
			t.Errorf("%s 没有返回参数错误: %v", testCase.name, testCase.err)
		}
	}

	name := current.Name()
	session := &migrationJournalSessionProbe{
		dialect: "postgres",
		queryResults: map[string][]map[string]interface{}{
			"FROM " + migrationJournalTable + " WHERE name": {{
				"name": name, "checksum": firstChecksum, "batch": int64(1),
				"operation": string(JournalOperationApply), "state": string(JournalReverted), "fencing_token": int64(1),
				"started_at": "2026-08-15T00:00:00Z", "finished_at": "2026-08-15T00:00:01Z", "failure_code": nil,
			}},
		},
	}
	lockedStore := newLockedJournalStore("postgres", session)
	if err := lockedStore.recordApplyingWithExecutor(context.Background(), session, current, 2, JournalOperationApply); err != nil {
		t.Fatalf("reverted journal 没有转为 applying: %v", err)
	}
	failingSession := &migrationJournalSessionProbe{dialect: "postgres", zeroFragment: "UPDATE " + migrationJournalTable}
	if err := lockedStore.recordFailedWithExecutor(context.Background(), failingSession, name, migrationFailureOperation); !errors.Is(err, ErrMigrationLockLost) {
		t.Fatalf("failed journal 零行更新没有报告锁丢失: %v", err)
	}
	zeroHistory := &migrationJournalSessionProbe{dialect: "postgres", zeroFragment: "DELETE FROM " + migrationHistoryTable}
	if err := lockedStore.deleteHistory(context.Background(), zeroHistory, current); !errors.Is(err, ErrMigrationDrift) {
		t.Fatalf("历史零行删除没有报告漂移: %v", err)
	}
	callback := func(Store) error { return nil }
	if err := (*DatabaseStore)(nil).WithMigrationLock(context.Background(), callback); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("空 Store 加锁错误不稳定: %v", err)
	}
	if err := store.WithMigrationLock(nilContext, callback); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("空上下文加锁错误不稳定: %v", err)
	}
	if err := store.WithMigrationLock(context.Background(), nil); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("空回调加锁错误不稳定: %v", err)
	}

	missing := newLockedJournalStore("postgres", &migrationJournalSessionProbe{dialect: "postgres"})
	if history, initialized, err := missing.Inspect(context.Background()); err != nil || initialized || history != nil {
		t.Fatalf("未初始化迁移表的 Inspect 结果错误: history=%#v initialized=%t err=%v", history, initialized, err)
	}
	if entries, err := missing.Journal(context.Background()); err != nil || entries != nil {
		t.Fatalf("未初始化 journal 的读取结果错误: entries=%#v err=%v", entries, err)
	}
	if _, err := NewDatabaseStore(db.NewDB(nil)); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("没有 SQL 方言的数据库没有被拒绝: %v", err)
	}
	createFailure := newLockedJournalStore("postgres", &migrationJournalSessionProbe{dialect: "postgres", failFragment: "CREATE TABLE"})
	if err := createFailure.Ensure(context.Background()); err == nil {
		t.Fatal("迁移表创建失败没有传播")
	}
	queryFailure := newLockedJournalStore("postgres", &migrationJournalSessionProbe{dialect: "postgres", failFragment: "information_schema.tables"})
	if err := queryFailure.Ensure(context.Background()); err == nil {
		t.Fatal("迁移表存在性查询失败没有传播")
	}
}

type sqliteBusyCodeError struct{ Code int64 }

func (err sqliteBusyCodeError) Error() string { return "driver sqlite error" }

// TestMigrationLockParserErrorsWithoutCGO 验证锁解析器对空结果、复合结果和驱动错误码的处理。
func TestMigrationLockParserErrorsWithoutCGO(t *testing.T) {
	if _, err := migrationLockValue(nil, "value"); !errors.Is(err, ErrMigrationLockUnavailable) {
		t.Fatalf("空锁结果没有被拒绝: %v", err)
	}
	if _, err := migrationOnlyInt([]map[string]interface{}{{"a": 1, "b": 2}}); !errors.Is(err, ErrMigrationLockUnavailable) {
		t.Fatalf("多列 busy_timeout 结果没有被拒绝: %v", err)
	}
	if _, err := migrationLockText([]map[string]interface{}{{"scope": " "}}, "scope"); !errors.Is(err, ErrMigrationLockUnavailable) {
		t.Fatalf("空锁作用域没有被拒绝: %v", err)
	}
	if !isSQLiteBusyOrLocked(sqliteBusyCodeError{Code: 5}) || !isSQLiteBusyOrLocked(sqliteBusyCodeError{Code: 6}) {
		t.Fatal("SQLite 驱动错误码没有被识别")
	}
	if isSQLiteBusyOrLocked(sqliteBusyCodeError{Code: 1}) {
		t.Fatal("无关 SQLite 驱动错误码被误判为锁冲突")
	}
}
