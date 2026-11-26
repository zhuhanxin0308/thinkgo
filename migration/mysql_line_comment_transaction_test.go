package migration

import (
	"context"
	"testing"
)

// TestMySQLCommentedDataMigrationSharesTransaction 检查注释不会改变业务 SQL 与元数据的事务边界。
func TestMySQLCommentedDataMigrationSharesTransaction(t *testing.T) {
	const insert = "-- note\rcontinued\nINSERT INTO accounts VALUES (1)"
	const remove = "# note\rcontinued\nDELETE FROM accounts WHERE id = 1"
	probe := &migrationDMLSessionProbe{migrationJournalSessionProbe: migrationJournalSessionProbe{dialect: "mysql"}}
	store := newLockedJournalStore("mysql", &probe.migrationJournalSessionProbe)
	store.session, store.executor = probe, probe
	current, err := NewSQL("202611260001_comment_data", []string{insert}, []string{remove})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.applyMigration(t.Context(), current, 1); err != nil {
		t.Fatal(err)
	}
	assertMigrationOperationsShareTransaction(t, probe.records, insert, "INSERT INTO "+migrationHistoryTable, "UPDATE "+migrationJournalTable)
	probe.records = nil
	if err := store.revertMigration(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	assertMigrationOperationsShareTransaction(t, probe.records, remove, "DELETE FROM "+migrationHistoryTable, "UPDATE "+migrationJournalTable)
}

// TestMySQLDeclaredCommentedMigration 检查显式事务型迁移的受限执行器接受带普通注释的 DML 和 SELECT。
func TestMySQLDeclaredCommentedMigration(t *testing.T) {
	const read = "-- note\rcontinued\nSELECT id FROM accounts WHERE id = ?"
	const write = "# note\rcontinued\nUPDATE accounts SET active = 1 WHERE id = ?"
	probe := &migrationDMLSessionProbe{migrationJournalSessionProbe: migrationJournalSessionProbe{dialect: "mysql"}}
	store := newLockedJournalStore("mysql", &probe.migrationJournalSessionProbe)
	store.session, store.executor = probe, probe
	current, err := NewTransactional("202611260002_comment_data", firstChecksum, func(ctx context.Context, executor Executor) error {
		if _, err := executor.QueryContext(ctx, read, int64(1)); err != nil {
			return err
		}
		_, err := executor.ExecuteContext(ctx, write, int64(1))
		return err
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.applyMigration(t.Context(), current, 1); err != nil {
		t.Fatal(err)
	}
	assertMigrationOperationsShareTransaction(t, probe.records, write, "INSERT INTO "+migrationHistoryTable, "UPDATE "+migrationJournalTable)
}
