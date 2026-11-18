package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

var migrationReadOnlySQLiteSeq atomic.Uint64

type inMemoryJournalRow struct {
	name         string
	checksum     string
	batch        int64
	operation    string
	state        string
	fencingToken int64
	startedAt    string
	finishedAt   interface{}
	failureCode  interface{}
}

type inMemoryHistoryRow struct {
	name      string
	checksum  string
	batch     int64
	appliedAt any
}

// statefulMigrationSQLSession 模拟完整的 fence/journal/history 表状态机，
// 在无 CGO 环境下确定性验证 Apply 失败 -> ResolveDirty(RecoveryMarkReverted) -> 修改 SQL -> 二次 Apply 全链路。
type statefulMigrationSQLSession struct {
	mu           sync.Mutex
	dialect      string
	tables       map[string]bool
	fencingToken int64
	journal      map[string]inMemoryJournalRow
	history      map[string]inMemoryHistoryRow
	failSQL      string
}

func newStatefulMigrationSQLSession(dialect string) *statefulMigrationSQLSession {
	return &statefulMigrationSQLSession{
		dialect: dialect,
		tables:  make(map[string]bool),
		journal: make(map[string]inMemoryJournalRow),
		history: make(map[string]inMemoryHistoryRow),
	}
}

func (s *statefulMigrationSQLSession) DialectName() string { return s.dialect }
func (s *statefulMigrationSQLSession) Invalidate()         {}

func (s *statefulMigrationSQLSession) TransactionContext(_ context.Context, callback func(db.ContextualRawQueryable) error) error {
	return callback(s)
}

func (s *statefulMigrationSQLSession) QueryContext(_ context.Context, query string, args ...interface{}) ([]map[string]interface{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case strings.Contains(query, "pg_advisory_lock"):
		return []map[string]interface{}{{"lock_result": nil}}, nil
	case strings.Contains(query, "pg_advisory_unlock"):
		return []map[string]interface{}{{"lock_result": true}}, nil
	case strings.Contains(query, "information_schema.tables"):
		tableName := fmt.Sprint(args[0])
		if s.tables[tableName] {
			return []map[string]interface{}{{"present": int64(1)}}, nil
		}
		return nil, nil
	case strings.Contains(query, "SELECT fencing_token FROM "+migrationFenceTable):
		if s.fencingToken <= 0 {
			return nil, nil
		}
		return []map[string]interface{}{{"fencing_token": s.fencingToken}}, nil
	case strings.Contains(query, "FROM "+migrationHistoryTable+" WHERE name = ?"):
		name := fmt.Sprint(args[0])
		row, ok := s.history[name]
		if !ok {
			return nil, nil
		}
		return []map[string]interface{}{{
			"name":       row.name,
			"checksum":   row.checksum,
			"batch":      row.batch,
			"applied_at": row.appliedAt,
		}}, nil
	case strings.Contains(query, "FROM "+migrationHistoryTable):
		rows := make([]map[string]interface{}, 0, len(s.history))
		for _, row := range s.history {
			rows = append(rows, map[string]interface{}{
				"name":       row.name,
				"checksum":   row.checksum,
				"batch":      row.batch,
				"applied_at": row.appliedAt,
			})
		}
		return rows, nil
	case strings.Contains(query, "FROM "+migrationJournalTable+" WHERE name = ?"):
		name := fmt.Sprint(args[0])
		row, ok := s.journal[name]
		if !ok {
			return nil, nil
		}
		return []map[string]interface{}{{
			"name":          row.name,
			"checksum":      row.checksum,
			"batch":         row.batch,
			"operation":     row.operation,
			"state":         row.state,
			"fencing_token": row.fencingToken,
			"started_at":    row.startedAt,
			"finished_at":   row.finishedAt,
			"failure_code":  row.failureCode,
		}}, nil
	case strings.Contains(query, "FROM "+migrationJournalTable):
		rows := make([]map[string]interface{}, 0, len(s.journal))
		for _, row := range s.journal {
			rows = append(rows, map[string]interface{}{
				"name":          row.name,
				"checksum":      row.checksum,
				"batch":         row.batch,
				"operation":     row.operation,
				"state":         row.state,
				"fencing_token": row.fencingToken,
				"started_at":    row.startedAt,
				"finished_at":   row.finishedAt,
				"failure_code":  row.failureCode,
			})
		}
		return rows, nil
	default:
		return nil, nil
	}
}

func (s *statefulMigrationSQLSession) ExecuteContext(_ context.Context, statement string, args ...interface{}) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failSQL != "" && strings.Contains(statement, s.failSQL) {
		return 0, errors.New("syntax error in migration SQL")
	}
	switch {
	case strings.HasPrefix(statement, "CREATE TABLE "+migrationHistoryTable):
		s.tables[migrationHistoryTable] = true
		return 0, nil
	case strings.HasPrefix(statement, "CREATE TABLE "+migrationJournalTable):
		s.tables[migrationJournalTable] = true
		return 0, nil
	case strings.HasPrefix(statement, "CREATE TABLE "+migrationFenceTable):
		s.tables[migrationFenceTable] = true
		return 0, nil
	case strings.HasPrefix(statement, "UPDATE "+migrationFenceTable+" SET fencing_token = fencing_token + 1"):
		if s.fencingToken == 0 {
			return 0, nil
		}
		s.fencingToken++
		return 1, nil
	case strings.HasPrefix(statement, "INSERT INTO "+migrationFenceTable):
		s.fencingToken = args[1].(int64)
		return 1, nil
	case strings.HasPrefix(statement, "INSERT INTO "+migrationJournalTable):
		token := args[5].(int64)
		if s.fencingToken != token {
			return 0, nil
		}
		name := fmt.Sprint(args[0])
		s.journal[name] = inMemoryJournalRow{
			name:         name,
			checksum:     fmt.Sprint(args[1]),
			batch:        args[2].(int64),
			operation:    fmt.Sprint(args[3]),
			state:        fmt.Sprint(args[4]),
			fencingToken: token,
			startedAt:    "2026-01-01T00:00:00Z",
			finishedAt:   args[7],
			failureCode:  args[8],
		}
		return 1, nil
	case strings.HasPrefix(statement, "UPDATE "+migrationJournalTable+" SET checksum = ?"):
		// recordApplyingWithExecutor 更新已有记录
		name := fmt.Sprint(args[8])
		prevToken := args[9].(int64)
		currToken := args[11].(int64)
		existing, ok := s.journal[name]
		if !ok || existing.fencingToken != prevToken || s.fencingToken != currToken {
			return 0, nil
		}
		s.journal[name] = inMemoryJournalRow{
			name:         name,
			checksum:     fmt.Sprint(args[0]),
			batch:        args[1].(int64),
			operation:    fmt.Sprint(args[2]),
			state:        fmt.Sprint(args[3]),
			fencingToken: args[4].(int64),
			startedAt:    "2026-01-01T00:00:01Z",
			finishedAt:   args[6],
			failureCode:  args[7],
		}
		return 1, nil
	case strings.HasPrefix(statement, "UPDATE "+migrationJournalTable+" SET state = ?, finished_at = ?, failure_code = ? WHERE name = ? AND fencing_token = ? AND state = ? AND operation = ?"):
		// finishJournal
		name := fmt.Sprint(args[3])
		token := args[4].(int64)
		existing, ok := s.journal[name]
		if !ok || existing.fencingToken != token || existing.state != fmt.Sprint(args[5]) || existing.operation != fmt.Sprint(args[6]) || s.fencingToken != args[8].(int64) {
			return 0, nil
		}
		existing.state = fmt.Sprint(args[0])
		existing.finishedAt = "2026-01-01T00:00:02Z"
		existing.failureCode = args[2]
		s.journal[name] = existing
		return 1, nil
	case strings.HasPrefix(statement, "UPDATE "+migrationJournalTable+" SET state = ?, finished_at = ?, failure_code = ? WHERE name = ? AND fencing_token = ? AND state = ?"):
		// recordFailedWithExecutor
		name := fmt.Sprint(args[3])
		token := args[4].(int64)
		existing, ok := s.journal[name]
		if !ok || existing.fencingToken != token || existing.state != fmt.Sprint(args[5]) || s.fencingToken != args[7].(int64) {
			return 0, nil
		}
		existing.state = fmt.Sprint(args[0])
		existing.finishedAt = "2026-01-01T00:00:02Z"
		existing.failureCode = args[2]
		s.journal[name] = existing
		return 1, nil
	case strings.HasPrefix(statement, "UPDATE "+migrationJournalTable+" SET operation = ?, state = ?, fencing_token = ?"):
		// finishRecovery
		name := fmt.Sprint(args[5])
		prevToken := args[6].(int64)
		existing, ok := s.journal[name]
		if !ok || existing.fencingToken != prevToken || s.fencingToken != args[10].(int64) {
			return 0, nil
		}
		existing.operation = fmt.Sprint(args[0])
		existing.state = fmt.Sprint(args[1])
		existing.fencingToken = args[2].(int64)
		existing.finishedAt = "2026-01-01T00:00:03Z"
		existing.failureCode = args[4]
		s.journal[name] = existing
		return 1, nil
	case strings.HasPrefix(statement, "INSERT INTO "+migrationHistoryTable):
		name := fmt.Sprint(args[0])
		var appliedAt any = "2026-01-01T00:00:02Z"
		if len(args) > 3 && args[3] != nil {
			appliedAt = args[3]
		}
		s.history[name] = inMemoryHistoryRow{
			name:      name,
			checksum:  fmt.Sprint(args[1]),
			batch:     args[2].(int64),
			appliedAt: appliedAt,
		}
		return 1, nil
	case strings.HasPrefix(statement, "DELETE FROM "+migrationHistoryTable):
		name := fmt.Sprint(args[0])
		checksum := fmt.Sprint(args[1])
		row, ok := s.history[name]
		if !ok || row.checksum != checksum {
			return 0, nil
		}
		delete(s.history, name)
		return 1, nil
	default:
		return 1, nil
	}
}

type sessionPinnedDatabaseStore struct {
	base    *DatabaseStore
	session db.PinnedSQLConnection
}

func (s *sessionPinnedDatabaseStore) Ensure(ctx context.Context) error { return s.base.Ensure(ctx) }
func (s *sessionPinnedDatabaseStore) Inspect(ctx context.Context) ([]History, bool, error) {
	return s.base.Inspect(ctx)
}
func (s *sessionPinnedDatabaseStore) Applied(ctx context.Context) ([]History, error) {
	return s.base.Applied(ctx)
}
func (s *sessionPinnedDatabaseStore) Apply(ctx context.Context, m Migration, batch int64) error {
	return s.base.Apply(ctx, m, batch)
}
func (s *sessionPinnedDatabaseStore) Revert(ctx context.Context, m Migration) error {
	return s.base.Revert(ctx, m)
}
func (s *sessionPinnedDatabaseStore) WithMigrationLock(ctx context.Context, cb func(Store) error) error {
	return s.base.withMigrationSession(ctx, s.session, cb)
}
func (s *sessionPinnedDatabaseStore) WithMigrationReadLock(ctx context.Context, cb func(Store) error) error {
	return s.base.withMigrationReadSession(ctx, s.session, cb)
}

// TestRevertedJournalAllowsModifiedMigrationToReapply 验证失败恢复或回滚至 reverted 后的迁移修改 SQL 后可重新应用 (#600)。
func TestRevertedJournalAllowsModifiedMigrationToReapply(t *testing.T) {
	const migName = "202601011200_create_users"
	session := newStatefulMigrationSQLSession("postgres")
	session.failSQL = "CREAT TABL broken_users"

	baseStore := &DatabaseStore{database: db.NewDB(nil), dialect: "postgres", executor: session, session: session}
	wrappedStore := &sessionPinnedDatabaseStore{base: baseStore, session: session}

	brokenMig, err := NewSQL(migName, []string{"CREAT TABL broken_users (id BIGINT PRIMARY KEY)"}, []string{"DROP TABLE broken_users"})
	if err != nil {
		t.Fatalf("构造初始迁移失败: %v", err)
	}
	reg1 := NewRegistry()
	if err := reg1.Register(brokenMig); err != nil {
		t.Fatal(err)
	}
	runner1, err := NewRunner(reg1, wrappedStore)
	if err != nil {
		t.Fatal(err)
	}

	// 1. 首次执行因 SQL 笔误失败，写入 failed journal。
	if _, err := runner1.Apply(context.Background()); err == nil {
		t.Fatal("预期 SQL 笔误导致首次 Apply 失败")
	}

	// 2. 运维核验未生效后通过 ResolveDirty(..., RecoveryMarkReverted) 恢复为 reverted。
	if err := runner1.ResolveDirty(context.Background(), migName, RecoveryMarkReverted); err != nil {
		t.Fatalf("ResolveDirty(RecoveryMarkReverted) 失败: %v", err)
	}

	// 3. 开发者修复 SQL（Checksum 改变），重新构建 Runner 并执行二次 Apply。
	fixedMig, err := NewSQL(migName, []string{"CREATE TABLE users (id BIGINT PRIMARY KEY)", "CREATE INDEX idx_users_id ON users(id)"}, []string{"DROP TABLE users"})
	if err != nil {
		t.Fatalf("构造修复后迁移失败: %v", err)
	}
	if fixedMig.Checksum() == brokenMig.Checksum() {
		t.Fatal("修复 SQL 后 Checksum 必须改变")
	}
	reg2 := NewRegistry()
	if err := reg2.Register(fixedMig); err != nil {
		t.Fatal(err)
	}
	runner2, err := NewRunner(reg2, wrappedStore)
	if err != nil {
		t.Fatal(err)
	}

	statuses, initialized, err := runner2.InspectStatus(context.Background())
	if err != nil || !initialized || len(statuses) != 1 || statuses[0].Applied {
		t.Fatalf("修复后预检状态异常: statuses=%+v initialized=%t err=%v", statuses, initialized, err)
	}

	appliedRes, err := runner2.Apply(context.Background())
	if err != nil || len(appliedRes.Names) != 1 || appliedRes.Names[0] != migName {
		t.Fatalf("修复 SQL 后二次 Apply 失败（不应误报 ErrMigrationDrift）: res=%+v err=%v", appliedRes, err)
	}

	entries, err := baseStore.Journal(context.Background())
	if err != nil || len(entries) != 1 || entries[0].State != JournalApplied || entries[0].Checksum != fixedMig.Checksum() {
		t.Fatalf("二次 Apply 后 journal 未更新为新 Checksum 与 applied: entries=%+v err=%v", entries, err)
	}

	// 4. 进一步验证 Rollback 回滚后再次修改 SQL 同样能重新 Apply，而已应用状态修改 SQL 仍严格拦截漂移。
	tamperedWhileApplied, err := NewSQL(migName, []string{"CREATE TABLE users (id BIGINT PRIMARY KEY, email TEXT)"}, []string{"DROP TABLE users"})
	if err != nil {
		t.Fatal(err)
	}
	regTampered := NewRegistry()
	_ = regTampered.Register(tamperedWhileApplied)
	runnerTampered, _ := NewRunner(regTampered, wrappedStore)
	if _, err := runnerTampered.Apply(context.Background()); !errors.Is(err, ErrMigrationDrift) {
		t.Fatalf("已应用迁移发生 Checksum 修改仍必须拦截 ErrMigrationDrift，实际为 %v", err)
	}

	if _, err := runner2.Rollback(context.Background(), 1); err != nil {
		t.Fatalf("正常回滚失败: %v", err)
	}
	if res, err := runnerTampered.Apply(context.Background()); err != nil || len(res.Names) != 1 {
		t.Fatalf("Rollback 回滚为 reverted 后修改 SQL 应可再次 Apply: res=%+v err=%v", res, err)
	}
}

// TestVariableLengthMigrationPrefixesOrderChronologically 验证 12~20 位变长数字前缀按真实时间前缀序执行与逆序回滚 (#600)。
func TestVariableLengthMigrationPrefixesOrderChronologically(t *testing.T) {
	m12, err := NewSQL("202601011200_create_users", []string{"SELECT 1"}, []string{"SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	m14, err := NewSQL("20260101120030_alter_users", []string{"SELECT 1"}, []string{"SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	m17, err := NewSQL("20260101120030500_seed_users", []string{"SELECT 1"}, []string{"SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	mNextDay12, err := NewSQL("202601020000_create_orders", []string{"SELECT 1"}, []string{"SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	for _, m := range []Migration{mNextDay12, m17, m14, m12} {
		if err := reg.Register(m); err != nil {
			t.Fatalf("注册变长前缀迁移 %s 失败: %v", m.Name(), err)
		}
	}

	session := newStatefulMigrationSQLSession("postgres")
	baseStore := &DatabaseStore{database: db.NewDB(nil), dialect: "postgres", executor: session, session: session}
	wrappedStore := &sessionPinnedDatabaseStore{base: baseStore, session: session}
	runner, err := NewRunner(reg, wrappedStore)
	if err != nil {
		t.Fatal(err)
	}

	wantOrder := []string{
		"202601011200_create_users",
		"20260101120030_alter_users",
		"20260101120030500_seed_users",
		"202601020000_create_orders",
	}

	statuses, err := runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status 失败: %v", err)
	}
	gotStatusNames := make([]string, len(statuses))
	for i, st := range statuses {
		gotStatusNames[i] = st.Name
	}
	if !reflect.DeepEqual(gotStatusNames, wantOrder) {
		t.Fatalf("Status 变长前缀顺序错误:\ngot:  %v\nwant: %v", gotStatusNames, wantOrder)
	}

	applied, err := runner.Apply(context.Background())
	if err != nil || !reflect.DeepEqual(applied.Names, wantOrder) {
		t.Fatalf("Apply 变长前缀顺序错误: got=%v want=%v err=%v", applied.Names, wantOrder, err)
	}

	historyRows, err := baseStore.Applied(context.Background())
	if err != nil || len(historyRows) != len(wantOrder) {
		t.Fatalf("Applied 历史读取失败: rows=%+v err=%v", historyRows, err)
	}
	for i, h := range historyRows {
		if h.Name != wantOrder[i] {
			t.Fatalf("Applied 历史排序错误: index=%d got=%s want=%s", i, h.Name, wantOrder[i])
		}
	}

	journalRows, err := baseStore.Journal(context.Background())
	if err != nil || len(journalRows) != len(wantOrder) {
		t.Fatalf("Journal 读取失败: rows=%+v err=%v", journalRows, err)
	}
	for i, j := range journalRows {
		if j.Name != wantOrder[i] {
			t.Fatalf("Journal 排序错误: index=%d got=%s want=%s", i, j.Name, wantOrder[i])
		}
	}

	rolledBack, err := runner.Rollback(context.Background(), 1)
	wantRollback := []string{
		"202601020000_create_orders",
		"20260101120030500_seed_users",
		"20260101120030_alter_users",
		"202601011200_create_users",
	}
	if err != nil || !reflect.DeepEqual(rolledBack.Names, wantRollback) {
		t.Fatalf("Rollback 变长前缀逆序错误:\ngot:  %v\nwant: %v\nerr:  %v", rolledBack.Names, wantRollback, err)
	}

	// 验证升级前已按旧 ASCII 顺序落盘的历史批次在回滚时严格遵循持久化 AppliedAt 逆序，不会先删表再撤销加列
	legacyCreate, err := NewSQL("20260101120030_create_users", []string{"SELECT 1"}, []string{"SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	legacyAlter, err := NewSQL("202601011200_add_email", []string{"SELECT 1"}, []string{"SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	legacyReg := NewRegistry()
	_ = legacyReg.Register(legacyCreate)
	_ = legacyReg.Register(legacyAlter)
	legacySession := newStatefulMigrationSQLSession("postgres")
	legacySession.tables[migrationHistoryTable] = true
	legacySession.tables[migrationJournalTable] = true
	legacySession.tables[migrationFenceTable] = true
	legacySession.fencingToken = 1
	legacySession.history[legacyCreate.Name()] = inMemoryHistoryRow{
		name: legacyCreate.Name(), checksum: legacyCreate.Checksum(), batch: 1, appliedAt: "2026-01-01T12:00:01Z",
	}
	legacySession.history[legacyAlter.Name()] = inMemoryHistoryRow{
		name: legacyAlter.Name(), checksum: legacyAlter.Checksum(), batch: 1, appliedAt: "2026-01-01T12:00:02Z",
	}
	legacySession.journal[legacyCreate.Name()] = inMemoryJournalRow{
		name: legacyCreate.Name(), checksum: legacyCreate.Checksum(), batch: 1, operation: "apply", state: "applied", fencingToken: 1, startedAt: "2026-01-01T12:00:01Z", finishedAt: "2026-01-01T12:00:01Z",
	}
	legacySession.journal[legacyAlter.Name()] = inMemoryJournalRow{
		name: legacyAlter.Name(), checksum: legacyAlter.Checksum(), batch: 1, operation: "apply", state: "applied", fencingToken: 1, startedAt: "2026-01-01T12:00:02Z", finishedAt: "2026-01-01T12:00:02Z",
	}
	legacyBase := &DatabaseStore{database: db.NewDB(nil), dialect: "postgres", executor: legacySession, session: legacySession}
	legacyRunner, err := NewRunner(legacyReg, &sessionPinnedDatabaseStore{base: legacyBase, session: legacySession})
	if err != nil {
		t.Fatal(err)
	}
	legacyRollback, err := legacyRunner.Rollback(context.Background(), 1)
	if err != nil || !reflect.DeepEqual(legacyRollback.Names, []string{legacyAlter.Name(), legacyCreate.Name()}) {
		t.Fatalf("历史批次 Rollback 未保留真实应用逆序: got=%v err=%v", legacyRollback.Names, err)
	}
}

type readOnlySQLiteDriver struct {
	mu         sync.Mutex
	statements []string
}

func (d *readOnlySQLiteDriver) Open(string) (driver.Conn, error) {
	return &readOnlySQLiteConn{driver: d}, nil
}

type readOnlySQLiteConn struct {
	driver *readOnlySQLiteDriver
}

func (c *readOnlySQLiteConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not supported")
}
func (c *readOnlySQLiteConn) Close() error              { return nil }
func (c *readOnlySQLiteConn) Begin() (driver.Tx, error) { return nil, errors.New("not supported") }

func (c *readOnlySQLiteConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.driver.mu.Lock()
	c.driver.statements = append(c.driver.statements, query)
	c.driver.mu.Unlock()
	upper := strings.ToUpper(strings.TrimSpace(query))
	if strings.Contains(upper, "IMMEDIATE") || strings.Contains(upper, "EXCLUSIVE") ||
		strings.HasPrefix(upper, "PRAGMA ") || strings.HasPrefix(upper, "CREATE ") ||
		strings.HasPrefix(upper, "INSERT ") || strings.HasPrefix(upper, "UPDATE ") ||
		strings.HasPrefix(upper, "DELETE ") {
		return nil, errors.New("attempt to write a readonly database")
	}
	return driver.RowsAffected(0), nil
}

func (c *readOnlySQLiteConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.driver.mu.Lock()
	c.driver.statements = append(c.driver.statements, query)
	c.driver.mu.Unlock()
	switch {
	case strings.Contains(query, "SELECT 1 FROM sqlite_master"):
		return &readOnlySQLiteRows{columns: []string{"1"}, rows: [][]driver.Value{{int64(1)}}}, nil
	case strings.Contains(query, "sqlite_master") && len(args) == 1 && args[0].Value == migrationHistoryTable:
		return &readOnlySQLiteRows{columns: []string{"name"}, rows: [][]driver.Value{{migrationHistoryTable}}}, nil
	case strings.Contains(query, "sqlite_master") && len(args) == 1 && args[0].Value == migrationJournalTable:
		return &readOnlySQLiteRows{columns: []string{"name"}, rows: [][]driver.Value{{migrationJournalTable}}}, nil
	case strings.Contains(query, "FROM "+migrationHistoryTable):
		return &readOnlySQLiteRows{
			columns: []string{"name", "checksum", "batch", "applied_at"},
			rows:    [][]driver.Value{{"202601011200_create_users", firstChecksum, int64(1), "2026-01-01T00:00:00Z"}},
		}, nil
	case strings.Contains(query, "FROM "+migrationJournalTable):
		return &readOnlySQLiteRows{
			columns: []string{"name", "checksum", "batch", "operation", "state", "fencing_token", "started_at", "finished_at", "failure_code"},
			rows:    [][]driver.Value{{"202601011200_create_users", firstChecksum, int64(1), "apply", "applied", int64(1), "2026-01-01T00:00:00Z", "2026-01-01T00:00:01Z", nil}},
		}, nil
	default:
		return &readOnlySQLiteRows{columns: []string{"name"}}, nil
	}
}

type readOnlySQLiteRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *readOnlySQLiteRows) Columns() []string { return r.columns }
func (r *readOnlySQLiteRows) Close() error      { return nil }
func (r *readOnlySQLiteRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}

// TestSQLiteInspectStatusUsesReadOnlyDeferredSnapshot 验证只读 SQLite 下 InspectStatus 不申请写锁而正常返回 (#600)。
func TestSQLiteInspectStatusUsesReadOnlyDeferredSnapshot(t *testing.T) {
	drv := &readOnlySQLiteDriver{}
	driverName := fmt.Sprintf("thinkgo_ro_sqlite_%d", migrationReadOnlySQLiteSeq.Add(1))
	sql.Register(driverName, drv)
	rawDB, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })

	database := db.NewDB(db.NewSQLConnection(rawDB, &builder.Sqlite{}))
	t.Cleanup(func() { _ = database.Close() })

	store, err := NewDatabaseStore(database)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := reg.Register(mustMigration(t, "202601011200_create_users", firstChecksum)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(mustMigration(t, "20260101120030_alter_users", secondChecksum)); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(reg, store)
	if err != nil {
		t.Fatal(err)
	}

	statuses, initialized, err := runner.InspectStatus(context.Background())
	if err != nil {
		t.Fatalf("只读 SQLite 上 InspectStatus 不应因写锁报错: %v", err)
	}
	if !initialized || len(statuses) != 2 || !statuses[0].Applied || statuses[1].Applied {
		t.Fatalf("只读 SQLite InspectStatus 结果不符: initialized=%t statuses=%+v", initialized, statuses)
	}

	drv.mu.Lock()
	joined := strings.Join(drv.statements, "\n")
	drv.mu.Unlock()
	if strings.Contains(joined, "BEGIN IMMEDIATE") {
		t.Fatalf("只读预检不应执行 BEGIN IMMEDIATE:\n%s", joined)
	}
	if !strings.Contains(joined, "BEGIN DEFERRED") || !strings.Contains(joined, "ROLLBACK") {
		t.Fatalf("只读预检应通过 BEGIN DEFERRED ... ROLLBACK 建立一致只读快照:\n%s", joined)
	}
}
