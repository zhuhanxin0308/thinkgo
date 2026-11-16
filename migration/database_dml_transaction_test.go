package migration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type migrationTransactionRecord struct {
	statement   string
	transaction int
}

type migrationDMLSessionProbe struct {
	migrationJournalSessionProbe
	active, transactions int
	records              []migrationTransactionRecord
}

func (probe *migrationDMLSessionProbe) TransactionContext(_ context.Context, fn func(db.ContextualRawQueryable) error) error {
	probe.transactions++
	probe.active = probe.transactions
	defer func() { probe.active = 0 }()
	return fn(probe)
}

func (probe *migrationDMLSessionProbe) ExecuteContext(ctx context.Context, statement string, args ...any) (int64, error) {
	probe.records = append(probe.records, migrationTransactionRecord{statement: statement, transaction: probe.active})
	return probe.migrationJournalSessionProbe.ExecuteContext(ctx, statement, args...)
}

func TestImplicitCommitDialectsRunDMLWithHistoryAndJournal(t *testing.T) {
	const insert = "INSERT INTO accounts (id, note) VALUES (1, 'demo; note')"
	const update = "UPDATE accounts SET note = 'changed' WHERE id = 1"
	const remove = "DELETE FROM accounts WHERE id = 1"
	for _, dialect := range []string{"mysql", "oracle"} {
		t.Run(dialect, func(t *testing.T) {
			probe := &migrationDMLSessionProbe{migrationJournalSessionProbe: migrationJournalSessionProbe{dialect: dialect}}
			store := newLockedJournalStore(dialect, &probe.migrationJournalSessionProbe)
			store.session, store.executor = probe, probe
			current, err := NewSQL("202611160001_account_data", []string{insert, update}, []string{remove})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.applyMigration(t.Context(), current, 1); err != nil {
				t.Fatal(err)
			}
			assertMigrationOperationsShareTransaction(t, probe.records, insert, update, "INSERT INTO "+migrationHistoryTable, "UPDATE "+migrationJournalTable)
			probe.records = nil
			if err := store.revertMigration(t.Context(), current); err != nil {
				t.Fatal(err)
			}
			assertMigrationOperationsShareTransaction(t, probe.records, remove, "DELETE FROM "+migrationHistoryTable, "UPDATE "+migrationJournalTable)
		})
	}
}

func assertMigrationOperationsShareTransaction(t *testing.T, records []migrationTransactionRecord, prefixes ...string) {
	t.Helper()
	if len(records) != len(prefixes) {
		t.Fatalf("迁移执行记录不完整: %#v", records)
	}
	transaction := records[0].transaction
	if transaction == 0 {
		t.Errorf("首条 DML 在事务外执行: %q", records[0].statement)
	}
	for index, prefix := range prefixes {
		if !strings.HasPrefix(records[index].statement, prefix) || records[index].transaction != transaction {
			t.Errorf("第 %d 条迁移操作未在同一事务执行: %#v", index+1, records[index])
		}
	}
}

func TestMySQLDataMigrationRejectsNonTransactionalMetadataBeforeRecording(t *testing.T) {
	const businessSQL = "INSERT INTO accounts VALUES (1)"
	current, err := NewSQL("202611160001_account_data", []string{businessSQL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, failQuery := range []bool{false, true} {
		probe := &migrationJournalSessionProbe{
			dialect: "mysql",
			queryResults: map[string][]map[string]any{
				"information_schema.ENGINES": {{"table_name": migrationHistoryTable}, {"table_name": migrationFenceTable}},
			},
		}
		if failQuery {
			probe.failFragment = "information_schema.ENGINES"
		}
		store := newLockedJournalStore("mysql", probe)
		if err := store.Apply(t.Context(), current, 1); !errors.Is(err, ErrInvalidMigration) {
			t.Fatalf("无法确认元表支持事务时应拒绝写入，查询失败=%t: %v", failQuery, err)
		}
		if probe.transactional || strings.Contains(strings.Join(probe.statements, "\n"), businessSQL) {
			t.Fatalf("元表检查失败就不能开始迁移: %#v", probe.statements)
		}
	}
}

func TestMixedSQLMigrationChoosesBoundaryPerDirection(t *testing.T) {
	const create = "CREATE TABLE accounts (id INTEGER PRIMARY KEY)"
	const insert = "INSERT INTO accounts VALUES (1)"
	const remove = "DELETE FROM accounts WHERE id = 1"
	current, err := NewSQL("202611160001_mixed_sql", []string{create, insert}, []string{remove})
	if err != nil {
		t.Fatal(err)
	}
	for _, dialect := range []string{"mysql", "oracle"} {
		t.Run(dialect, func(t *testing.T) {
			probe := &migrationDMLSessionProbe{migrationJournalSessionProbe: migrationJournalSessionProbe{dialect: dialect}}
			store := newLockedJournalStore(dialect, &probe.migrationJournalSessionProbe)
			store.session, store.executor = probe, probe
			if err := store.applyMigration(t.Context(), current, 1); err != nil {
				t.Fatal(err)
			}
			if len(probe.records) != 4 || probe.records[0].transaction != 0 || probe.records[1].transaction != 0 || probe.records[2].transaction == 0 {
				t.Fatalf("混合迁移的 Up 应保留非事务保护: %#v", probe.records)
			}
			probe.records = nil
			if err := store.revertMigration(t.Context(), current); err != nil {
				t.Fatal(err)
			}
			assertMigrationOperationsShareTransaction(t, probe.records, remove, "DELETE FROM "+migrationHistoryTable, "UPDATE "+migrationJournalTable)
		})
	}
}

func TestDeclaredGoMigrationRejectsImplicitCommitsAndAllowsDML(t *testing.T) {
	const insert = "INSERT INTO accounts VALUES (?)"
	const goodRead = "SELECT id FROM accounts WHERE id = ?"
	for _, dialect := range []string{"mysql", "oracle"} {
		t.Run(dialect, func(t *testing.T) {
			probe := &migrationDMLSessionProbe{migrationJournalSessionProbe: migrationJournalSessionProbe{dialect: dialect}}
			store := newLockedJournalStore(dialect, &probe.migrationJournalSessionProbe)
			store.session, store.executor = probe, probe
			up := func(ctx context.Context, executor Executor) error {
				if _, err := executor.QueryContext(ctx, goodRead, int64(1)); err != nil {
					return err
				}
				_, err := executor.ExecuteContext(ctx, insert, int64(1))
				return err
			}
			name := "202611160001_declared_data"
			plain, err := New(name, firstChecksum, up, nil)
			if err != nil {
				t.Fatal(err)
			}
			if atomic, _ := migrationTransactionMode(plain, JournalOperationApply, dialect); atomic {
				t.Fatal("未声明的 Go 迁移不能自动改变边界")
			}
			declared, err := NewTransactional(name, firstChecksum, up, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.applyMigration(t.Context(), declared, 1); err != nil {
				t.Fatal(err)
			}
			assertMigrationOperationsShareTransaction(t, probe.records, insert, "INSERT INTO "+migrationHistoryTable, "UPDATE "+migrationJournalTable)
			for _, useQuery := range []bool{false, true} {
				probe.records = nil
				bad, err := NewTransactional(name, firstChecksum, func(ctx context.Context, executor Executor) error {
					const ddl = "DROP TABLE accounts"
					if useQuery {
						_, err := executor.QueryContext(ctx, ddl)
						return err
					}
					_, err := executor.ExecuteContext(ctx, ddl)
					return err
				}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.applyMigration(t.Context(), bad, 1); !errors.Is(err, ErrInvalidMigration) || len(probe.records) != 0 {
					t.Fatalf("DDL 不能进入声明为纯数据迁移的事务: %v %#v", err, probe.records)
				}
			}
		})
	}
	if invalid, err := NewTransactional("invalid", firstChecksum, nil, nil); invalid != nil || !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("非法声明应返回明确错误: %#v %v", invalid, err)
	}
}
