package migration

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type migrationJournalSessionProbe struct {
	dialect       string
	statements    []string
	failFragment  string
	zeroFragment  string
	queryResults  map[string][]map[string]interface{}
	transactional bool
}

func (session *migrationJournalSessionProbe) DialectName() string { return session.dialect }
func (*migrationJournalSessionProbe) Invalidate()                 {}

func (session *migrationJournalSessionProbe) TransactionContext(_ context.Context, callback func(db.ContextualRawQueryable) error) error {
	session.transactional = true
	return callback(session)
}

func (session *migrationJournalSessionProbe) QueryContext(_ context.Context, statement string, _ ...interface{}) ([]map[string]interface{}, error) {
	session.statements = append(session.statements, statement)
	if session.failFragment != "" && strings.Contains(statement, session.failFragment) {
		return nil, errors.New("query failed")
	}
	for fragment, rows := range session.queryResults {
		if strings.Contains(statement, fragment) {
			return rows, nil
		}
	}
	return nil, nil
}

func (session *migrationJournalSessionProbe) ExecuteContext(_ context.Context, statement string, _ ...interface{}) (int64, error) {
	session.statements = append(session.statements, statement)
	if session.failFragment != "" && strings.Contains(statement, session.failFragment) {
		return 0, errors.New("execute failed")
	}
	if session.zeroFragment != "" && strings.Contains(statement, session.zeroFragment) {
		return 0, nil
	}
	return 1, nil
}

func newLockedJournalStore(dialect string, session *migrationJournalSessionProbe) *DatabaseStore {
	return &DatabaseStore{
		database:     db.NewDB(nil),
		dialect:      dialect,
		executor:     session,
		session:      session,
		fencingToken: 7,
	}
}

// TestNonTransactionalDialectJournalsBeforeDDL 验证 MySQL/Oracle 在任何 DDL 前持久化 applying。
func TestNonTransactionalDialectJournalsBeforeDDL(t *testing.T) {
	for _, dialect := range []string{"mysql", "oracle"} {
		t.Run(dialect, func(t *testing.T) {
			session := &migrationJournalSessionProbe{dialect: dialect}
			store := newLockedJournalStore(dialect, session)
			current, err := NewSQL(
				"202608150001_create_accounts",
				[]string{"CREATE TABLE accounts (id BIGINT PRIMARY KEY)"},
				[]string{"DROP TABLE accounts"},
			)
			if err != nil {
				t.Fatalf("创建非事务迁移失败: %v", err)
			}
			if err = store.Apply(context.Background(), current, 1); err != nil {
				t.Fatalf("执行非事务迁移失败: %v", err)
			}
			joined := strings.Join(session.statements, "\n")
			journalIndex := strings.Index(joined, "INSERT INTO "+migrationJournalTable)
			ddlIndex := strings.Index(joined, "CREATE TABLE accounts")
			historyIndex := strings.Index(joined, "INSERT INTO "+migrationHistoryTable)
			finishIndex := strings.LastIndex(joined, "UPDATE "+migrationJournalTable)
			if journalIndex < 0 || ddlIndex <= journalIndex || historyIndex <= ddlIndex || finishIndex <= historyIndex {
				t.Fatalf("非事务方言 journal/DDL/history 顺序错误:\n%s", joined)
			}
			if !session.transactional {
				t.Fatal("历史与 journal 最终态没有放入 DML 事务")
			}
		})
	}
}

// TestNonTransactionalDialectKeepsDirtyWhenHistoryFails 验证 DDL 成功但历史失败后会留下 failed journal。
func TestNonTransactionalDialectKeepsDirtyWhenHistoryFails(t *testing.T) {
	session := &migrationJournalSessionProbe{dialect: "mysql", failFragment: "INSERT INTO " + migrationHistoryTable}
	store := newLockedJournalStore("mysql", session)
	current, err := NewSQL(
		"202608150001_create_accounts",
		[]string{"CREATE TABLE accounts (id BIGINT PRIMARY KEY)"},
		nil,
	)
	if err != nil {
		t.Fatalf("创建历史失败迁移失败: %v", err)
	}
	if err = store.Apply(context.Background(), current, 1); err == nil {
		t.Fatal("历史写入失败必须传播")
	}
	joined := strings.Join(session.statements, "\n")
	if !strings.Contains(joined, "CREATE TABLE accounts") || !strings.Contains(joined, "SET state = ?") {
		t.Fatalf("DDL 后没有写 failed journal:\n%s", joined)
	}
}

// TestJournalFinalizationRequiresCurrentFencingToken 验证最终态更新零行时必须报告旧 owner 已失效。
func TestJournalFinalizationRequiresCurrentFencingToken(t *testing.T) {
	session := &migrationJournalSessionProbe{dialect: "postgres", zeroFragment: "UPDATE " + migrationJournalTable}
	store := newLockedJournalStore("postgres", session)
	err := store.finishJournal(context.Background(), session, "202608150001_create_accounts", JournalOperationApply, JournalApplied)
	if !errors.Is(err, ErrMigrationLockLost) {
		t.Fatalf("旧 fencing owner 没有被拒绝: %v", err)
	}
}

// TestDatabaseStoreRejectsUnlockedWrites 验证公开 Store 写入口不能绕过数据库级迁移锁。
func TestDatabaseStoreRejectsUnlockedWrites(t *testing.T) {
	store := &DatabaseStore{database: db.NewDB(nil), dialect: "sqlite"}
	current := mustMigration(t, "202608150001_create_accounts", firstChecksum)
	if err := store.Apply(context.Background(), current, 1); !errors.Is(err, ErrMigrationLockRequired) {
		t.Fatalf("未加锁 Apply 没有被拒绝: %v", err)
	}
	if err := store.Revert(context.Background(), current); !errors.Is(err, ErrMigrationLockRequired) {
		t.Fatalf("未加锁 Revert 没有被拒绝: %v", err)
	}
}

// TestMigrationSchemaDefinitionsCoverJournalAndFence 验证五方言均生成 history/journal/fence 三张表。
func TestMigrationSchemaDefinitionsCoverJournalAndFence(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres", "sqlite", "sqlserver", "oracle"} {
		t.Run(dialect, func(t *testing.T) {
			definitions, err := migrationTableDefinitions(dialect)
			if err != nil || len(definitions) != 3 {
				t.Fatalf("方言迁移表定义不完整: definitions=%#v err=%v", definitions, err)
			}
			joined := definitions[0].createSQL + definitions[1].createSQL + definitions[2].createSQL
			for _, required := range []string{migrationHistoryTable, migrationJournalTable, migrationFenceTable, "fencing_token"} {
				if !strings.Contains(joined, required) {
					t.Fatalf("%s 缺少 %s: %s", dialect, required, joined)
				}
			}
		})
	}
}

// TestNonTransactionalDialectRevertFinalizesHistory 验证非事务 DDL 回滚后才在 DML 事务中删除历史并完成 journal。
func TestNonTransactionalDialectRevertFinalizesHistory(t *testing.T) {
	session := &migrationJournalSessionProbe{dialect: "oracle"}
	store := newLockedJournalStore("oracle", session)
	current, err := NewSQL(
		"202608150001_drop_accounts",
		[]string{"CREATE TABLE accounts (id NUMBER PRIMARY KEY)"},
		[]string{"DROP TABLE accounts"},
	)
	if err != nil {
		t.Fatalf("创建非事务回滚迁移失败: %v", err)
	}
	if err = store.revertMigration(context.Background(), current); err != nil {
		t.Fatalf("执行非事务回滚失败: %v", err)
	}
	joined := strings.Join(session.statements, "\n")
	downIndex := strings.Index(joined, "DROP TABLE accounts")
	deleteIndex := strings.Index(joined, "DELETE FROM "+migrationHistoryTable)
	finishIndex := strings.LastIndex(joined, "UPDATE "+migrationJournalTable)
	if downIndex < 0 || deleteIndex <= downIndex || finishIndex <= deleteIndex || !session.transactional {
		t.Fatalf("非事务回滚顺序错误:\n%s", joined)
	}
}

// TestDatabaseStoreRevertUsesAppliedBatchAndJournalTransition 验证完整 Revert 入口复用历史批次并从 applied 转为 applying。
func TestDatabaseStoreRevertUsesAppliedBatchAndJournalTransition(t *testing.T) {
	name := "202608150001_drop_accounts"
	current, err := NewSQL(
		name,
		[]string{"CREATE TABLE accounts (id BIGINT PRIMARY KEY)"},
		[]string{"DROP TABLE accounts"},
	)
	if err != nil {
		t.Fatalf("创建完整 Revert 迁移失败: %v", err)
	}
	session := &migrationJournalSessionProbe{
		dialect: "mysql",
		queryResults: map[string][]map[string]interface{}{
			"FROM " + migrationHistoryTable + " WHERE name": {{
				"name": name, "checksum": current.Checksum(), "batch": int64(4), "applied_at": "2026-08-15T00:00:00Z",
			}},
			"FROM " + migrationJournalTable + " WHERE name": {{
				"name": name, "checksum": current.Checksum(), "batch": int64(4),
				"operation": string(JournalOperationApply), "state": string(JournalApplied), "fencing_token": int64(6),
			}},
		},
	}
	store := newLockedJournalStore("mysql", session)
	if err = store.Revert(context.Background(), current); err != nil {
		t.Fatalf("执行完整 Revert 失败: %v", err)
	}
	joined := strings.Join(session.statements, "\n")
	if !strings.Contains(joined, "SET checksum = ?") || !strings.Contains(joined, "DROP TABLE accounts") || !strings.Contains(joined, "DELETE FROM "+migrationHistoryTable) {
		t.Fatalf("完整 Revert 没有执行 journal/DDL/history 状态机:\n%s", joined)
	}
}

// TestMigrationValueConversionsCoverDriverRepresentations 验证五类驱动常见整数、文本和可空时间表示。
func TestMigrationValueConversionsCoverDriverRepresentations(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value interface{}
		want  int64
	}{
		{name: "signed", value: int32(7), want: 7},
		{name: "unsigned", value: uint16(8), want: 8},
		{name: "string", value: "9", want: 9},
		{name: "bytes", value: []byte("10"), want: 10},
		{name: "integral_float", value: float64(11), want: 11},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := migrationInt64(testCase.value)
			if err != nil || got != testCase.want {
				t.Fatalf("迁移整数转换错误: got=%d err=%v", got, err)
			}
		})
	}
	if _, err := migrationInt64(uint64(math.MaxUint64)); err == nil {
		t.Fatal("溢出无符号整数必须被拒绝")
	}
	if _, err := migrationInt64(uint64(math.MaxInt64) + 1); err == nil {
		t.Fatal("刚好超过 int64 上界的无符号整数必须被拒绝")
	}
	if _, err := migrationInt64(struct{}{}); err == nil {
		t.Fatal("未知整数类型必须被拒绝")
	}
	if _, err := migrationInt64(1.5); err == nil {
		t.Fatal("非整数浮点 token 必须被拒绝")
	}
	if _, err := migrationInt64(float64(math.MaxInt64)); err == nil {
		t.Fatal("超出 int64 的浮点 token 必须被拒绝")
	}
	if text, err := migrationText([]byte("driver-text")); err != nil || text != "driver-text" {
		t.Fatalf("字节文本转换错误: text=%q err=%v", text, err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if text, err := migrationText(now); err != nil || text != now.Format(time.RFC3339Nano) {
		t.Fatalf("时间文本转换错误: text=%q err=%v", text, err)
	}
	if text, err := migrationOptionalText(nil); err != nil || text != "" {
		t.Fatalf("可空文本转换错误: text=%q err=%v", text, err)
	}
	if value := migrationRowValue(map[string]interface{}{"FENCING_TOKEN": int64(12)}, "fencing_token"); value != int64(12) {
		t.Fatalf("大写方言列名没有按大小写无关读取: %#v", value)
	}
}

// TestDatabaseStoreRejectsInvalidConstructionAndLockInputs 验证迁移存储不会在依赖或上下文非法时进入锁流程。
func TestDatabaseStoreRejectsInvalidConstructionAndLockInputs(t *testing.T) {
	if _, err := NewDatabaseStore(nil); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("空数据库构造错误不稳定: %v", err)
	}
	if err := (*DatabaseStore)(nil).WithMigrationLock(context.Background(), func(Store) error { return nil }); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("空 Store 锁错误不稳定: %v", err)
	}
	store := &DatabaseStore{database: db.NewDB(nil), dialect: "sqlite"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.WithMigrationLock(ctx, func(Store) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消迁移锁上下文没有传播: %v", err)
	}
	if _, err := migrationTableDefinitions("unknown"); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("未知迁移方言没有被拒绝: %v", err)
	}
}
