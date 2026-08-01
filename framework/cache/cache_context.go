package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

func deleteCacheValueContext(ctx context.Context, driver Driver, key string) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if contextual, ok := driver.(ContextualDeleter); ok {
		return contextual.DeleteContext(ctx, key)
	}
	return driver.Delete(key)
}

func getCacheValueContext(ctx context.Context, driver Driver, key string) (interface{}, bool, error) {
	if ctx == nil {
		return nil, false, ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if contextual, ok := driver.(ContextualGetter); ok {
		return contextual.GetContext(ctx, key)
	}
	return driver.Get(key)
}

func setCacheValueContext(ctx context.Context, driver Driver, key string, value interface{}, ttl time.Duration) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if contextual, ok := driver.(ContextualSetter); ok {
		return contextual.SetContext(ctx, key, value, ttl)
	}
	return driver.Set(key, value, ttl)
}

func clearCacheDriverContext(ctx context.Context, driver Driver) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if contextual, ok := driver.(ContextualClearer); ok {
		return contextual.ClearContext(ctx)
	}
	return driver.Clear()
}

// withTagMutationLockContext 在有限窗口内获取标签变更锁，并响应调用方取消。
func (c *Cache) withTagMutationLockContext(ctx context.Context, driver Driver, callback func() error) (resultErr error) {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.state == nil {
		return ErrCacheDriverNotConfigured
	}
	locker, supported := driver.(DistributedLocker)
	if !supported {
		if err := ctx.Err(); err != nil {
			return err
		}
		return callback()
	}
	owner, err := newLockOwner(c.storeName)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCacheLock, err)
	}
	lockKey := lockKeyPrefix + "tag:" + c.storeName
	acquired, err := acquireDistributedLockContext(ctx, locker, lockKey, owner, tagMutationLockTTL, tagMutationWait)
	if err != nil {
		return err
	}
	if !acquired {
		return ErrCacheLockBusy
	}
	defer func() {
		released, releaseErr := releaseDistributedLock(context.WithoutCancel(ctx), locker, lockKey, owner)
		if releaseErr == nil && !released {
			releaseErr = ErrCacheLockLost
		}
		resultErr = errors.Join(resultErr, releaseErr)
	}()
	if renewer, supported := driver.(LockRenewer); supported {
		renewal := startLockRenewal(func() (bool, error) {
			return renewDistributedLock(context.WithoutCancel(ctx), renewer, locker, lockKey, owner, tagMutationLockTTL)
		}, tagMutationLockTTL)
		defer func() {
			resultErr = errors.Join(resultErr, renewal.stop())
		}()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return callback()
}

type lockRenewalSession struct {
	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan error
	stopErr  error
}

// startLockRenewal 按租约的三分之一周期续租，并在续租失败时将锁标记为丢失。
func startLockRenewal(renew func() (bool, error), ttl time.Duration) *lockRenewalSession {
	session := &lockRenewalSession{
		stopCh: make(chan struct{}),
		done:   make(chan error, 1),
	}
	interval := ttl / 3
	if interval < lockRetryInterval {
		interval = lockRetryInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				renewed, err := renew()
				if err != nil {
					session.done <- err
					return
				}
				if !renewed {
					session.done <- ErrCacheLockLost
					return
				}
			case <-session.stopCh:
				session.done <- nil
				return
			}
		}
	}()
	return session
}

// stop 停止续租并等待最后一次续租完成，避免释放锁与续租请求交错。
func (s *lockRenewalSession) stop() error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.stopErr = <-s.done
	})
	return s.stopErr
}

// acquireDistributedLockContext 在等待重试间隔内监听请求取消，且优先使用驱动的上下文接口。
func acquireDistributedLockContext(ctx context.Context, locker DistributedLocker, key, owner string, ttl, wait time.Duration) (bool, error) {
	if ctx == nil {
		return false, ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	deadline := time.Now().Add(wait)
	for {
		acquired, err := acquireDistributedLockAttempt(ctx, locker, key, owner, ttl)
		if err != nil || acquired {
			return acquired, err
		}
		if wait <= 0 || !time.Now().Before(deadline) {
			return false, nil
		}
		remaining := time.Until(deadline)
		interval := lockRetryInterval
		if remaining < interval {
			interval = remaining
		}
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return false, ctx.Err()
		}
	}
}

func acquireDistributedLockAttempt(ctx context.Context, locker DistributedLocker, key, owner string, ttl time.Duration) (bool, error) {
	if contextual, ok := locker.(ContextualDistributedLocker); ok {
		return contextual.AcquireLockContext(ctx, key, owner, ttl)
	}
	return locker.AcquireLock(key, owner, ttl)
}

func releaseDistributedLock(ctx context.Context, locker DistributedLocker, key, owner string) (bool, error) {
	if contextual, ok := locker.(ContextualDistributedLocker); ok {
		return contextual.ReleaseLockContext(ctx, key, owner)
	}
	return locker.ReleaseLock(key, owner)
}

func renewDistributedLock(ctx context.Context, renewer LockRenewer, locker DistributedLocker, key, owner string, ttl time.Duration) (bool, error) {
	if contextual, ok := locker.(ContextualDistributedLocker); ok {
		return contextual.RenewLockContext(ctx, key, owner, ttl)
	}
	return renewer.RenewLock(key, owner, ttl)
}
