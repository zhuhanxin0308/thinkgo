package migration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

// WithMigrationLock 在同一物理 SQL 会话中完成加锁、schema/journal 初始化、fencing 和全部迁移操作。
func (store *DatabaseStore) WithMigrationLock(ctx context.Context, callback func(Store) error) error {
	if store == nil || store.database == nil || ctx == nil || callback == nil {
		return fmt.Errorf("%w: 迁移锁参数非法", ErrInvalidMigration)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return store.database.WithPinnedSQLConnection(ctx, func(session db.PinnedSQLConnection) error {
		return store.withMigrationSession(ctx, session, callback)
	})
}

// WithMigrationReadLock 在不建表、不推进 fencing token 的前提下取得同一数据库级锁，提供一致的只读快照边界。
func (store *DatabaseStore) WithMigrationReadLock(ctx context.Context, callback func(Store) error) error {
	if store == nil || store.database == nil || ctx == nil || callback == nil {
		return fmt.Errorf("%w: 迁移只读锁参数非法", ErrInvalidMigration)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return store.database.WithPinnedSQLConnection(ctx, func(session db.PinnedSQLConnection) error {
		return store.withMigrationReadSession(ctx, session, callback)
	})
}

// withMigrationSession 集中管理锁生命周期，便于验证主错误、解锁错误与会话失效语义。
func (store *DatabaseStore) withMigrationSession(ctx context.Context, session db.PinnedSQLConnection, callback func(Store) error) (resultErr error) {
	return store.withAcquiredMigrationSession(ctx, session, func() (bool, error) {
		bound := &DatabaseStore{
			database:     store.database,
			dialect:      store.dialect,
			executor:     session,
			session:      session,
			sqliteLocked: store.dialect == "sqlite",
		}
		if err := bound.Ensure(ctx); err != nil {
			return false, err
		}
		token, err := bound.nextFencingToken(ctx)
		if err != nil {
			return false, err
		}
		bound.fencingToken = token
		// callback 返回错误时仍需提交其已经持久化的 failed/applying journal。
		return true, callback(bound)
	})
}

func (store *DatabaseStore) withMigrationReadSession(ctx context.Context, session db.PinnedSQLConnection, callback func(Store) error) error {
	return store.withAcquiredMigrationSession(ctx, session, func() (bool, error) {
		bound := &DatabaseStore{database: store.database, dialect: store.dialect, executor: session, session: session}
		callbackErr := callback(bound)
		return callbackErr == nil, callbackErr
	})
}

func (store *DatabaseStore) withAcquiredMigrationSession(
	ctx context.Context,
	session db.PinnedSQLConnection,
	callback func() (safeSQLiteCommit bool, resultErr error),
) (resultErr error) {
	if store == nil || store.database == nil || ctx == nil || session == nil || callback == nil {
		return fmt.Errorf("%w: 固定迁移会话参数非法", ErrInvalidMigration)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if session.DialectName() != store.dialect {
		session.Invalidate()
		return fmt.Errorf("%w: pinned 会话方言从 %q 变为 %q", ErrMigrationLockLost, store.dialect, session.DialectName())
	}
	locker, err := newDialectMigrationLocker(session)
	if err != nil {
		return err
	}
	acquireCtx, cancelAcquire := context.WithTimeout(ctx, defaultMigrationLockTimeout)
	deadline, _ := acquireCtx.Deadline()
	lockTimeout := time.Until(deadline)
	err = locker.Acquire(acquireCtx, lockTimeout)
	cancelAcquire()
	if err != nil {
		// 加锁请求报错时无法排除“服务端已加锁但响应丢失”，必须丢弃物理会话。
		session.Invalidate()
		return err
	}

	safeSQLiteCommit := false
	defer func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), migrationCleanupTimeout)
		defer cancelCleanup()
		if recovered := recover(); recovered != nil {
			if releaseErr := locker.Release(cleanupCtx, false); releaseErr != nil {
				session.Invalidate()
			}
			panic(recovered)
		}
		commit := true
		if store.dialect == "sqlite" {
			commit = safeSQLiteCommit
		}
		releaseErr := locker.Release(cleanupCtx, commit)
		if releaseErr != nil {
			session.Invalidate()
		}
		resultErr = errors.Join(resultErr, releaseErr)
	}()
	safeSQLiteCommit, resultErr = callback()
	return resultErr
}
