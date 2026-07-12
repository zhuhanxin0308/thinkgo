package driver

import (
	"fmt"
	"sync"
	"time"
)

const memorySweepInterval = time.Minute

// Memory 是支持 nil 命中、严格计数和独立锁空间的进程内缓存驱动。
type Memory struct {
	items     map[string]item
	locks     map[string]memoryLock
	lock      sync.RWMutex
	sequence  uint64
	lastSweep time.Time
}

type item struct {
	value   interface{}
	expiry  time.Time
	version uint64
}

type memoryLock struct {
	owner  string
	expiry time.Time
}

// NewMemory 创建内存缓存驱动。
func NewMemory() *Memory {
	return &Memory{items: make(map[string]item), locks: make(map[string]memoryLock)}
}

func (c *Memory) Get(key string) (interface{}, bool, error) {
	if c == nil {
		return nil, false, fmt.Errorf("内存缓存驱动为空")
	}
	c.lock.RLock()
	stored, found := c.items[key]
	c.lock.RUnlock()
	if !found {
		return nil, false, nil
	}
	if !stored.expiry.IsZero() && !time.Now().Before(stored.expiry) {
		c.lock.Lock()
		current, exists := c.items[key]
		if exists && current.version == stored.version {
			delete(c.items, key)
		}
		c.lock.Unlock()
		return nil, false, nil
	}
	return stored.value, true, nil
}

func (c *Memory) Set(key string, value interface{}, ttl time.Duration) error {
	if c == nil {
		return fmt.Errorf("内存缓存驱动为空")
	}
	if err := validateDriverTTL(ttl); err != nil {
		return err
	}
	now := time.Now()
	expiry := time.Time{}
	if ttl > 0 {
		expiry = now.Add(ttl)
	}
	c.lock.Lock()
	c.ensureMapsLocked()
	c.sweepExpiredLocked(now)
	c.sequence++
	c.items[key] = item{value: value, expiry: expiry, version: c.sequence}
	c.lock.Unlock()
	return nil
}

func (c *Memory) Has(key string) (bool, error) {
	_, found, err := c.Get(key)
	return found, err
}

func (c *Memory) Delete(key string) error {
	if c == nil {
		return fmt.Errorf("内存缓存驱动为空")
	}
	c.lock.Lock()
	delete(c.items, key)
	c.lock.Unlock()
	return nil
}

func (c *Memory) Clear() error {
	if c == nil {
		return fmt.Errorf("内存缓存驱动为空")
	}
	c.lock.Lock()
	c.items = make(map[string]item)
	c.lock.Unlock()
	return nil
}

func (c *Memory) Inc(key string, step int64) (int64, error) {
	if c == nil {
		return 0, fmt.Errorf("内存缓存驱动为空")
	}
	if err := validateCounterStep(step); err != nil {
		return 0, err
	}
	now := time.Now()
	c.lock.Lock()
	defer c.lock.Unlock()
	c.ensureMapsLocked()
	c.sweepExpiredLocked(now)

	stored, found := c.items[key]
	if found && !stored.expiry.IsZero() && !now.Before(stored.expiry) {
		delete(c.items, key)
		stored = item{}
		found = false
	}
	current := int64(0)
	if found {
		var err error
		current, err = strictCounterValue(stored.value)
		if err != nil {
			return 0, err
		}
	}
	updated, err := checkedCounterAdd(current, step)
	if err != nil {
		return 0, err
	}
	c.sequence++
	c.items[key] = item{value: updated, expiry: stored.expiry, version: c.sequence}
	return updated, nil
}

func (c *Memory) Dec(key string, step int64) (int64, error) {
	if c == nil {
		return 0, fmt.Errorf("内存缓存驱动为空")
	}
	if err := validateCounterStep(step); err != nil {
		return 0, err
	}
	now := time.Now()
	c.lock.Lock()
	defer c.lock.Unlock()
	c.ensureMapsLocked()
	c.sweepExpiredLocked(now)

	stored, found := c.items[key]
	if found && !stored.expiry.IsZero() && !now.Before(stored.expiry) {
		delete(c.items, key)
		stored = item{}
		found = false
	}
	current := int64(0)
	if found {
		var err error
		current, err = strictCounterValue(stored.value)
		if err != nil {
			return 0, err
		}
	}
	updated, err := checkedCounterSubtract(current, step)
	if err != nil {
		return 0, err
	}
	c.sequence++
	c.items[key] = item{value: updated, expiry: stored.expiry, version: c.sequence}
	return updated, nil
}

// AcquireLock 获取进程内锁，过期锁会在同一临界区内被替换。
func (c *Memory) AcquireLock(key string, owner string, ttl time.Duration) (bool, error) {
	if c == nil || owner == "" || ttl <= 0 {
		return false, ErrInvalidCacheLock
	}
	now := time.Now()
	c.lock.Lock()
	defer c.lock.Unlock()
	c.ensureMapsLocked()
	c.sweepExpiredLocked(now)
	if existing, found := c.locks[key]; found && existing.expiry.After(now) {
		return false, nil
	}
	c.locks[key] = memoryLock{owner: owner, expiry: now.Add(ttl)}
	return true, nil
}

// ReleaseLock 仅允许未过期锁的 owner 释放锁。
func (c *Memory) ReleaseLock(key string, owner string) (bool, error) {
	if c == nil || owner == "" {
		return false, ErrInvalidCacheLock
	}
	now := time.Now()
	c.lock.Lock()
	defer c.lock.Unlock()
	existing, found := c.locks[key]
	if !found {
		return false, nil
	}
	if !existing.expiry.After(now) {
		delete(c.locks, key)
		return false, nil
	}
	if existing.owner != owner {
		return false, nil
	}
	delete(c.locks, key)
	return true, nil
}

func (c *Memory) ensureMapsLocked() {
	if c.items == nil {
		c.items = make(map[string]item)
	}
	if c.locks == nil {
		c.locks = make(map[string]memoryLock)
	}
}

func (c *Memory) sweepExpiredLocked(now time.Time) {
	if !c.lastSweep.IsZero() && now.Sub(c.lastSweep) < memorySweepInterval {
		return
	}
	for key, stored := range c.items {
		if !stored.expiry.IsZero() && !now.Before(stored.expiry) {
			delete(c.items, key)
		}
	}
	for key, lock := range c.locks {
		if !lock.expiry.After(now) {
			delete(c.locks, key)
		}
	}
	c.lastSweep = now
}
