package driver

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Update 在内存驱动写锁内完成单键读改写，避免并发 Session 合并丢失更新。
func (c *Memory) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	return c.updateAtomic(key, ttl, false, update)
}

// UpdatePreserveTTL 在同一写锁内更新值，并保留命中项原有的绝对过期时间。
func (c *Memory) UpdatePreserveTTL(key string, update func(interface{}, bool) (interface{}, bool, error)) error {
	return c.updateAtomic(key, 0, true, update)
}

// UpdatePreserveTTLConditionally 在持锁回调中决定是否保留 TTL；逻辑失效项可按永久新值提交。
func (c *Memory) UpdatePreserveTTLConditionally(key string, update func(interface{}, bool) (interface{}, bool, bool, error)) error {
	return c.updateAtomicTTLPolicy(key, 0, update)
}

func (c *Memory) updateAtomic(key string, ttl time.Duration, preserveTTL bool, update func(interface{}, bool) (interface{}, bool, error)) error {
	var policy func(interface{}, bool) (interface{}, bool, bool, error)
	if update != nil {
		policy = func(value interface{}, found bool) (interface{}, bool, bool, error) {
			next, remove, err := update(value, found)
			return next, remove, preserveTTL, err
		}
	}
	return c.updateAtomicTTLPolicy(key, ttl, policy)
}

func (c *Memory) updateAtomicTTLPolicy(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, bool, error)) error {
	if c == nil {
		return fmt.Errorf("内存缓存驱动为空")
	}
	if err := validateDriverTTL(ttl); err != nil {
		return err
	}
	if update == nil {
		return ErrNilAtomicUpdate
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.updateAtomicLocked(key, ttl, update)
}

// updateAtomicLocked 在唯一内存锁内完成值和 TTL 更新，调用方已经取得互斥权。
func (c *Memory) updateAtomicLocked(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, bool, error)) error {
	now := time.Now()
	c.ensureMapsLocked()
	c.sweepExpiredLocked(now)

	stored, found := c.items[key]
	if found && !stored.expiry.IsZero() && !now.Before(stored.expiry) {
		c.removeItemLocked(key, stored)
		stored = item{}
		found = false
	}
	var current interface{}
	if found {
		current = cloneMemoryValue(stored.value)
	}
	next, remove, preserveTTL, err := update(current, found)
	if err != nil {
		return err
	}
	if remove {
		if found {
			c.removeItemLocked(key, stored)
		}
		return nil
	}
	expiry := stored.expiry
	if !preserveTTL {
		expiry = time.Time{}
	}
	if !preserveTTL && ttl > 0 {
		expiry = now.Add(ttl)
	}
	c.sequence++
	return c.storeItemLocked(key, cloneMemoryValue(next), expiry, c.sequence, isMetadataKey(key))
}

// UpdateIfLockOwnerContext 将租约判定与缓存写入放在同一互斥区间内。
func (c *Memory) UpdateIfLockOwnerContext(ctx context.Context, key string, ttl time.Duration, lockKey, owner string, update func(interface{}, bool) (interface{}, bool, error)) (bool, error) {
	if c == nil || ctx == nil || lockKey == "" || owner == "" {
		return false, ErrInvalidCacheLock
	}
	if err := validateDriverTTL(ttl); err != nil {
		return false, err
	}
	if update == nil {
		return false, ErrNilAtomicUpdate
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	lease, exists := c.locks[lockKey]
	if !exists || lease.owner != owner || !lease.expiry.After(time.Now()) {
		return false, nil
	}
	err := c.updateAtomicLocked(key, ttl, func(value interface{}, found bool) (interface{}, bool, bool, error) {
		next, remove, err := update(value, found)
		return next, remove, false, err
	})
	return err == nil, err
}

// ClearPrefix 只删除属于指定字面量前缀的数据；锁使用独立空间，不受清理影响。
func (c *Memory) ClearPrefix(prefix string) error {
	if c == nil {
		return fmt.Errorf("内存缓存驱动为空")
	}
	if prefix == "" {
		return ErrInvalidCachePrefix
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	for key, stored := range c.items {
		if strings.HasPrefix(key, prefix) && !isCapacityExemptKey(key) {
			c.removeItemLocked(key, stored)
		}
	}
	return nil
}
