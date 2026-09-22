package migration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

func (store *DatabaseStore) nextFencingToken(ctx context.Context) (int64, error) {
	if store == nil || store.session == nil {
		return 0, ErrMigrationLockRequired
	}
	var token int64
	err := store.runAtomic(ctx, func(executor Executor) error {
		var tokenErr error
		token, tokenErr = store.nextFencingTokenWithExecutor(ctx, executor)
		return tokenErr
	})
	return token, err
}

func (store *DatabaseStore) nextFencingTokenWithExecutor(ctx context.Context, executor Executor) (int64, error) {
	affected, err := executor.ExecuteContext(
		ctx,
		"UPDATE "+migrationFenceTable+" SET fencing_token = fencing_token + 1 WHERE lock_name = ?",
		migrationFenceLockName,
	)
	if err != nil {
		return 0, fmt.Errorf("增加迁移 fencing token 失败: %w", err)
	}
	if affected == 0 {
		if _, err = executor.ExecuteContext(
			ctx,
			"INSERT INTO "+migrationFenceTable+" (lock_name, fencing_token) VALUES (?, ?)",
			migrationFenceLockName,
			int64(1),
		); err != nil {
			return 0, fmt.Errorf("初始化迁移 fencing token 失败: %w", err)
		}
	}
	rows, err := executor.QueryContext(
		ctx,
		"SELECT fencing_token FROM "+migrationFenceTable+" WHERE lock_name = ?",
		migrationFenceLockName,
	)
	if err != nil || len(rows) != 1 {
		return 0, fmt.Errorf("读取迁移 fencing token 失败: %w", errors.Join(err, ErrMigrationLockLost))
	}
	token, err := migrationInt64(migrationRowValue(rows[0], "fencing_token"))
	if err != nil || token <= 0 {
		return 0, fmt.Errorf("%w: fencing token 非法", ErrMigrationLockLost)
	}
	return token, nil
}

// Journal 返回完整 durable journal；旧数据库尚未创建 journal 表时返回空结果。
func (store *DatabaseStore) Journal(ctx context.Context) ([]JournalEntry, error) {
	if store == nil || store.database == nil || ctx == nil {
		return nil, fmt.Errorf("%w: journal 参数非法", ErrInvalidMigration)
	}
	definitions, err := migrationTableDefinitions(store.dialect)
	if err != nil {
		return nil, err
	}
	exists, err := store.tableExists(ctx, definitions[1])
	if err != nil || !exists {
		return nil, err
	}
	rows, err := store.activeExecutor().QueryContext(
		ctx,
		"SELECT name, checksum, batch, operation, state, fencing_token, started_at, finished_at, failure_code FROM "+migrationJournalTable+" ORDER BY name ASC",
	)
	if err != nil {
		return nil, err
	}
	entries := make([]JournalEntry, 0, len(rows))
	for index, row := range rows {
		name, nameErr := migrationText(migrationRowValue(row, "name"))
		checksum, checksumErr := migrationText(migrationRowValue(row, "checksum"))
		batch, batchErr := migrationInt64(migrationRowValue(row, "batch"))
		operation, operationErr := migrationText(migrationRowValue(row, "operation"))
		state, stateErr := migrationText(migrationRowValue(row, "state"))
		token, tokenErr := migrationInt64(migrationRowValue(row, "fencing_token"))
		startedAt, startedErr := migrationText(migrationRowValue(row, "started_at"))
		finishedAt, finishedErr := migrationOptionalText(migrationRowValue(row, "finished_at"))
		failureCode, failureErr := migrationOptionalText(migrationRowValue(row, "failure_code"))
		if nameErr != nil || checksumErr != nil || batchErr != nil || operationErr != nil || stateErr != nil || tokenErr != nil || startedErr != nil || finishedErr != nil || failureErr != nil {
			return nil, fmt.Errorf("%w: 第 %d 条迁移 journal 字段非法", ErrMigrationDrift, index+1)
		}
		entries = append(entries, JournalEntry{
			Name: name, Checksum: checksum, Batch: batch,
			Operation: JournalOperation(operation), State: JournalState(state), FencingToken: token,
			StartedAt: startedAt, FinishedAt: finishedAt, FailureCode: failureCode,
		})
	}
	return entries, nil
}

func (store *DatabaseStore) recordApplying(ctx context.Context, current Migration, batch int64, operation JournalOperation) error {
	return store.runAtomic(ctx, func(executor Executor) error {
		return store.recordApplyingWithExecutor(ctx, executor, current, batch, operation)
	})
}

func (store *DatabaseStore) recordApplyingWithExecutor(ctx context.Context, executor Executor, current Migration, batch int64, operation JournalOperation) error {
	existing, exists, err := store.journalByNameWithExecutor(ctx, executor, current.Name())
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if !exists {
		affected, insertErr := executor.ExecuteContext(
			ctx,
			"INSERT INTO "+migrationJournalTable+" (name, checksum, batch, operation, state, fencing_token, started_at, finished_at, failure_code) SELECT ?, ?, ?, ?, ?, ?, ?, ?, ? FROM "+migrationFenceTable+" WHERE lock_name = ? AND fencing_token = ?",
			current.Name(), current.Checksum(), batch, string(operation), string(JournalApplying), store.fencingToken, now, nil, nil,
			migrationFenceLockName, store.fencingToken,
		)
		if insertErr != nil {
			return fmt.Errorf("写入 applying journal 失败: %w", insertErr)
		}
		if affected != 1 {
			return fmt.Errorf("%w: applying journal 未命中当前全局 fencing token", ErrMigrationLockLost)
		}
		return nil
	}
	if existing.FencingToken >= store.fencingToken {
		return fmt.Errorf("%w: journal token=%d current=%d", ErrMigrationLockLost, existing.FencingToken, store.fencingToken)
	}
	if existing.State == JournalApplying || existing.State == JournalFailed {
		return fmt.Errorf("%w: %s state=%s", ErrMigrationDirty, current.Name(), existing.State)
	}
	if existing.Checksum != current.Checksum() {
		return fmt.Errorf("%w: %s", ErrMigrationDrift, current.Name())
	}
	validTransition := operation == JournalOperationApply && existing.State == JournalReverted ||
		operation == JournalOperationRevert && existing.State == JournalApplied
	if !validTransition {
		return fmt.Errorf("%w: journal 状态 %s 不能执行 %s", ErrMigrationDrift, existing.State, operation)
	}
	affected, err := executor.ExecuteContext(
		ctx,
		"UPDATE "+migrationJournalTable+" SET checksum = ?, batch = ?, operation = ?, state = ?, fencing_token = ?, started_at = ?, finished_at = ?, failure_code = ? WHERE name = ? AND fencing_token = ? AND EXISTS (SELECT 1 FROM "+migrationFenceTable+" WHERE lock_name = ? AND fencing_token = ?)",
		current.Checksum(), batch, string(operation), string(JournalApplying), store.fencingToken, now, nil, nil, current.Name(), existing.FencingToken,
		migrationFenceLockName, store.fencingToken,
	)
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: applying journal 被更新持有者覆盖", ErrMigrationLockLost)
	}
	return nil
}

func (store *DatabaseStore) recordFailed(ctx context.Context, name, failureCode string) error {
	return store.runAtomic(ctx, func(executor Executor) error {
		return store.recordFailedWithExecutor(ctx, executor, name, failureCode)
	})
}

func (store *DatabaseStore) recordFailedWithExecutor(ctx context.Context, executor Executor, name, failureCode string) error {
	affected, err := executor.ExecuteContext(
		ctx,
		"UPDATE "+migrationJournalTable+" SET state = ?, finished_at = ?, failure_code = ? WHERE name = ? AND fencing_token = ? AND state = ? AND EXISTS (SELECT 1 FROM "+migrationFenceTable+" WHERE lock_name = ? AND fencing_token = ?)",
		string(JournalFailed), time.Now().UTC(), failureCode, name, store.fencingToken, string(JournalApplying),
		migrationFenceLockName, store.fencingToken,
	)
	if err != nil {
		return fmt.Errorf("记录 failed journal 失败: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: failed journal 未命中当前 fencing token", ErrMigrationLockLost)
	}
	return nil
}

func (store *DatabaseStore) applyMigration(ctx context.Context, current Migration, batch int64) error {
	if store.dialect == "mysql" || store.dialect == "oracle" {
		if err := current.Up(ctx, store.activeExecutor()); err != nil {
			return err
		}
		return store.runAtomic(ctx, func(executor Executor) error {
			if err := store.insertHistory(ctx, executor, current.Name(), current.Checksum(), batch); err != nil {
				return err
			}
			return store.finishJournal(ctx, executor, current.Name(), JournalOperationApply, JournalApplied)
		})
	}
	return store.runAtomic(ctx, func(executor Executor) error {
		if err := current.Up(ctx, executor); err != nil {
			return err
		}
		if err := store.insertHistory(ctx, executor, current.Name(), current.Checksum(), batch); err != nil {
			return err
		}
		return store.finishJournal(ctx, executor, current.Name(), JournalOperationApply, JournalApplied)
	})
}

func (store *DatabaseStore) revertMigration(ctx context.Context, current Migration) error {
	if store.dialect == "mysql" || store.dialect == "oracle" {
		if err := current.Down(ctx, store.activeExecutor()); err != nil {
			return err
		}
		return store.runAtomic(ctx, func(executor Executor) error {
			if err := store.deleteHistory(ctx, executor, current); err != nil {
				return err
			}
			return store.finishJournal(ctx, executor, current.Name(), JournalOperationRevert, JournalReverted)
		})
	}
	return store.runAtomic(ctx, func(executor Executor) error {
		if err := current.Down(ctx, executor); err != nil {
			return err
		}
		if err := store.deleteHistory(ctx, executor, current); err != nil {
			return err
		}
		return store.finishJournal(ctx, executor, current.Name(), JournalOperationRevert, JournalReverted)
	})
}

func (store *DatabaseStore) runAtomic(ctx context.Context, callback func(Executor) error) error {
	if store.sqliteLocked {
		return store.runSQLiteSavepoint(ctx, callback)
	}
	return store.session.TransactionContext(ctx, func(executor db.ContextualRawQueryable) error {
		return callback(executor)
	})
}

func (store *DatabaseStore) runSQLiteSavepoint(ctx context.Context, callback func(Executor) error) (resultErr error) {
	if _, err := store.activeExecutor().ExecuteContext(ctx, "SAVEPOINT "+sqliteMigrationSavepoint); err != nil {
		return err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), migrationCleanupTimeout)
			_, _ = store.activeExecutor().ExecuteContext(cleanupCtx, "ROLLBACK TO SAVEPOINT "+sqliteMigrationSavepoint)
			_, _ = store.activeExecutor().ExecuteContext(cleanupCtx, "RELEASE SAVEPOINT "+sqliteMigrationSavepoint)
			cancel()
			panic(recovered)
		}
	}()
	if callbackErr := callback(store.activeExecutor()); callbackErr != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), migrationCleanupTimeout)
		_, rollbackErr := store.activeExecutor().ExecuteContext(cleanupCtx, "ROLLBACK TO SAVEPOINT "+sqliteMigrationSavepoint)
		_, releaseErr := store.activeExecutor().ExecuteContext(cleanupCtx, "RELEASE SAVEPOINT "+sqliteMigrationSavepoint)
		cancel()
		return errors.Join(callbackErr, rollbackErr, releaseErr)
	}
	_, err := store.activeExecutor().ExecuteContext(ctx, "RELEASE SAVEPOINT "+sqliteMigrationSavepoint)
	return err
}

func (store *DatabaseStore) insertHistory(ctx context.Context, executor Executor, name, checksum string, batch int64) error {
	_, err := executor.ExecuteContext(
		ctx,
		"INSERT INTO "+migrationHistoryTable+" (name, checksum, batch, applied_at) VALUES (?, ?, ?, ?)",
		name, checksum, batch, time.Now().UTC(),
	)
	return err
}

func (store *DatabaseStore) deleteHistory(ctx context.Context, executor Executor, current Migration) error {
	affected, err := executor.ExecuteContext(
		ctx,
		"DELETE FROM "+migrationHistoryTable+" WHERE name = ? AND checksum = ?",
		current.Name(), current.Checksum(),
	)
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: 迁移 %s 的历史删除行数为 %d", ErrMigrationDrift, current.Name(), affected)
	}
	return nil
}

func (store *DatabaseStore) finishJournal(ctx context.Context, executor Executor, name string, operation JournalOperation, state JournalState) error {
	affected, err := executor.ExecuteContext(
		ctx,
		"UPDATE "+migrationJournalTable+" SET state = ?, finished_at = ?, failure_code = ? WHERE name = ? AND fencing_token = ? AND state = ? AND operation = ? AND EXISTS (SELECT 1 FROM "+migrationFenceTable+" WHERE lock_name = ? AND fencing_token = ?)",
		string(state), time.Now().UTC(), nil, name, store.fencingToken, string(JournalApplying), string(operation),
		migrationFenceLockName, store.fencingToken,
	)
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: journal 最终写入未命中当前 fencing token", ErrMigrationLockLost)
	}
	return nil
}

func (store *DatabaseStore) journalByName(ctx context.Context, name string) (JournalEntry, bool, error) {
	return store.journalByNameWithExecutor(ctx, store.activeExecutor(), name)
}

func (store *DatabaseStore) journalByNameWithExecutor(ctx context.Context, executor Executor, name string) (JournalEntry, bool, error) {
	rows, err := executor.QueryContext(
		ctx,
		"SELECT name, checksum, batch, operation, state, fencing_token, started_at, finished_at, failure_code FROM "+migrationJournalTable+" WHERE name = ?",
		name,
	)
	if err != nil {
		return JournalEntry{}, false, err
	}
	if len(rows) == 0 {
		return JournalEntry{}, false, nil
	}
	if len(rows) != 1 {
		return JournalEntry{}, false, fmt.Errorf("%w: journal %s 重复", ErrMigrationDrift, name)
	}
	row := rows[0]
	checksum, checksumErr := migrationText(migrationRowValue(row, "checksum"))
	batch, batchErr := migrationInt64(migrationRowValue(row, "batch"))
	operation, operationErr := migrationText(migrationRowValue(row, "operation"))
	state, stateErr := migrationText(migrationRowValue(row, "state"))
	token, tokenErr := migrationInt64(migrationRowValue(row, "fencing_token"))
	if checksumErr != nil || batchErr != nil || operationErr != nil || stateErr != nil || tokenErr != nil {
		return JournalEntry{}, false, fmt.Errorf("%w: journal %s 字段非法", ErrMigrationDrift, name)
	}
	return JournalEntry{Name: name, Checksum: checksum, Batch: batch, Operation: JournalOperation(operation), State: JournalState(state), FencingToken: token}, true, nil
}

func (store *DatabaseStore) historyByName(ctx context.Context, name string) (History, error) {
	rows, err := store.activeExecutor().QueryContext(
		ctx,
		"SELECT name, checksum, batch, applied_at FROM "+migrationHistoryTable+" WHERE name = ?",
		name,
	)
	if err != nil {
		return History{}, fmt.Errorf("读取迁移历史 %s 失败: %w", name, err)
	}
	if len(rows) != 1 {
		return History{}, fmt.Errorf("%w: 迁移历史 %s 不存在或重复", ErrMigrationDrift, name)
	}
	checksum, checksumErr := migrationText(migrationRowValue(rows[0], "checksum"))
	batch, batchErr := migrationInt64(migrationRowValue(rows[0], "batch"))
	if checksumErr != nil || batchErr != nil {
		return History{}, fmt.Errorf("%w: 迁移历史 %s 字段非法", ErrMigrationDrift, name)
	}
	return History{Name: name, Checksum: checksum, Batch: batch}, nil
}

// ResolveDirty 使用新的 fencing token 条件更新 journal，并只修改 journal/history，不声称约束任意 DDL。
func (store *DatabaseStore) ResolveDirty(ctx context.Context, name string, resolution RecoveryResolution) error {
	if !store.locked() || ctx == nil || !migrationNamePattern.MatchString(name) {
		return ErrMigrationLockRequired
	}
	entry, exists, err := store.journalByName(ctx, name)
	if err != nil {
		return err
	}
	if !exists || entry.State != JournalApplying && entry.State != JournalFailed {
		return fmt.Errorf("%w: %s", ErrMigrationNotDirty, name)
	}
	if entry.FencingToken >= store.fencingToken {
		return fmt.Errorf("%w: 恢复 token=%d current=%d", ErrMigrationLockLost, entry.FencingToken, store.fencingToken)
	}
	return store.runAtomic(ctx, func(executor Executor) error {
		switch resolution {
		case RecoveryMarkApplied:
			if err := store.ensureRecoveredHistory(ctx, executor, entry); err != nil {
				return err
			}
			return store.finishRecovery(ctx, executor, entry, JournalOperationApply, JournalApplied)
		case RecoveryMarkReverted:
			if err := store.deleteRecoveredHistory(ctx, executor, entry); err != nil {
				return err
			}
			return store.finishRecovery(ctx, executor, entry, JournalOperationRevert, JournalReverted)
		default:
			return fmt.Errorf("%w: 未知恢复结论 %q", ErrInvalidMigration, resolution)
		}
	})
}

func (store *DatabaseStore) ensureRecoveredHistory(ctx context.Context, executor Executor, entry JournalEntry) error {
	rows, err := executor.QueryContext(ctx, "SELECT checksum, batch FROM "+migrationHistoryTable+" WHERE name = ?", entry.Name)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return store.insertHistory(ctx, executor, entry.Name, entry.Checksum, entry.Batch)
	}
	if len(rows) != 1 {
		return fmt.Errorf("%w: 恢复历史 %s 重复", ErrMigrationDrift, entry.Name)
	}
	checksum, checksumErr := migrationText(migrationRowValue(rows[0], "checksum"))
	batch, batchErr := migrationInt64(migrationRowValue(rows[0], "batch"))
	if checksumErr != nil || batchErr != nil || checksum != entry.Checksum || batch != entry.Batch {
		return fmt.Errorf("%w: 恢复历史 %s 与 journal 不一致", ErrMigrationDrift, entry.Name)
	}
	return nil
}

func (store *DatabaseStore) deleteRecoveredHistory(ctx context.Context, executor Executor, entry JournalEntry) error {
	rows, err := executor.QueryContext(ctx, "SELECT checksum, batch FROM "+migrationHistoryTable+" WHERE name = ?", entry.Name)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	if len(rows) != 1 {
		return fmt.Errorf("%w: 恢复历史 %s 重复", ErrMigrationDrift, entry.Name)
	}
	checksum, checksumErr := migrationText(migrationRowValue(rows[0], "checksum"))
	batch, batchErr := migrationInt64(migrationRowValue(rows[0], "batch"))
	if checksumErr != nil || batchErr != nil || checksum != entry.Checksum || batch != entry.Batch {
		return fmt.Errorf("%w: 恢复历史 %s 与 journal 不一致", ErrMigrationDrift, entry.Name)
	}
	_, err = executor.ExecuteContext(ctx, "DELETE FROM "+migrationHistoryTable+" WHERE name = ? AND checksum = ?", entry.Name, entry.Checksum)
	return err
}

func (store *DatabaseStore) finishRecovery(ctx context.Context, executor Executor, entry JournalEntry, operation JournalOperation, state JournalState) error {
	affected, err := executor.ExecuteContext(
		ctx,
		"UPDATE "+migrationJournalTable+" SET operation = ?, state = ?, fencing_token = ?, finished_at = ?, failure_code = ? WHERE name = ? AND fencing_token = ? AND state IN (?, ?) AND EXISTS (SELECT 1 FROM "+migrationFenceTable+" WHERE lock_name = ? AND fencing_token = ?)",
		string(operation), string(state), store.fencingToken, time.Now().UTC(), nil, entry.Name, entry.FencingToken, string(JournalApplying), string(JournalFailed),
		migrationFenceLockName, store.fencingToken,
	)
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: dirty 恢复被更新持有者覆盖", ErrMigrationLockLost)
	}
	return nil
}

func migrationOptionalText(value interface{}) (string, error) {
	if value == nil {
		return "", nil
	}
	text, err := migrationText(value)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}
