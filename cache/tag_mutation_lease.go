package cache

import "context"

type tagMutationLeaseKey struct{}

type tagMutationLease struct {
	key   string
	owner string
}

// writeLeasedTagMetadata 把持有者检查与元数据写入放在同一后端原子区间。
// 回滚沿用同一租约；丢锁后不能用旧快照覆盖新持有者的成员。
func writeLeasedTagMetadata(ctx context.Context, driver Driver, lease tagMutationLease, key string, values []string) error {
	if !cacheDriverSupportsGuardedUpdates(driver) {
		// 旧锁驱动仍可执行普通缓存操作，但不能宣称提供跨进程安全的标签写入。
		return ErrCacheLockUnsupported
	}
	committed, err := driver.(LockGuardedUpdater).UpdateIfLockOwnerContext(ctx, key, 0, lease.key, lease.owner,
		func(interface{}, bool) (interface{}, bool, error) { return values, len(values) == 0, nil })
	if err != nil {
		return err
	}
	if !committed {
		return ErrCacheLockLost
	}
	return nil
}
