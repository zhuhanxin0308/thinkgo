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
	return store.withAcquiredMigrationSession(ctx, session, false, func() (bool, error) {
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
		callbackErr := callback(bound)
		if cleanupErr := bound.sqliteSavepointFailure(); cleanupErr != nil {
			// 业务层可能捕获 panic 或忽略返回错误；保存点清理不确定时仍必须回滚整笔事务。
			if !errors.Is(callbackErr, cleanupErr) {
				callbackErr = errors.Join(callbackErr, cleanupErr)
			}
			return false, callbackErr
		}
		// 保存点成功回滚的业务错误仍需提交其已经持久化的 failed/applying journal。
		return true, callbackErr
	})
}

func (store *DatabaseStore) withMigrationReadSession(ctx context.Context, session db.PinnedSQLConnection, callback func(Store) error) error {
	return store.withAcquiredMigrationSession(ctx, session, true, func() (bool, error) {
		bound := &DatabaseStore{database: store.database, dialect: store.dialect, executor: session, session: session}
		callbackErr := callback(bound)
		return callbackErr == nil, callbackErr
	})
}

func (store *DatabaseStore) withAcquiredMigrationSession(
	ctx context.Context,
	session db.PinnedSQLConnection,
	readOnly bool,
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
	var (
		locker dialectMigrationLocker
		err    error
	)
	if readOnly {
		locker, err = newDialectMigrationReadLocker(session)
	} else {
		locker, err = newDialectMigrationLocker(session)
	}
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
	callbackReturned := false
	defer func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), migrationCleanupTimeout)
		defer cancelCleanup()
		// 只用实际返回标记决定能否提交，禁止 recover 吞掉旧式 nil panic。
		commit := callbackReturned
		if store.dialect == "sqlite" {
			commit = callbackReturned && safeSQLiteCommit
		}
		releaseErr := locker.Release(cleanupCtx, commit)
		if releaseErr != nil {
			session.Invalidate()
		}
		resultErr = errors.Join(resultErr, releaseErr)
	}()
	safeSQLiteCommit, resultErr = callback()
	callbackReturned = true
	return resultErr
}
