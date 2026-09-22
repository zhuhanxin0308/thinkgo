package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// UpdateIfLockOwnerContext 持有同一租约文件句柄直到数据提交，接管者必须等待该 OS 锁释放。
func (c *File) UpdateIfLockOwnerContext(ctx context.Context, key string, ttl time.Duration, lockKey, owner string, update func(interface{}, bool) (interface{}, bool, error)) (committed bool, resultErr error) {
	if c == nil || c.path == "" || ctx == nil || lockKey == "" || owner == "" {
		return false, ErrInvalidCacheLock
	}
	if err := validateDriverTTL(ttl); err != nil {
		return false, err
	}
	if update == nil {
		return false, ErrNilAtomicUpdate
	}
	c.operationMu.RLock()
	defer c.operationMu.RUnlock()
	resultErr = c.withMutationGuardContext(ctx, key, func() (err error) {
		lock := c.keyLock(key)
		lock.Lock()
		defer lock.Unlock()
		// 不嵌套取得另一把进程分片锁，避免数据键与锁键哈希到同一分片时自锁。
		handle, _, err := c.openLockedCacheLockFile(c.lockFilePath(lockKey))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, closeLockedCacheLockFile(handle)) }()
		if err := ctx.Err(); err != nil {
			return err
		}
		lease, err := readCacheLockPayload(handle)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidCacheLock, err)
		}
		if lease == nil || lease.Owner != owner || !lease.Expiry.After(time.Now()) {
			return nil
		}
		err = c.updateItemLocked(key, ttl, func(value interface{}, found bool) (interface{}, bool, bool, error) {
			next, remove, err := update(value, found)
			return next, remove, false, err
		})
		committed = err == nil
		return err
	})
	return committed, resultErr
}
