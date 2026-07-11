package driver

import (
	"sync"
	"time"
)

// Memory cache driver
type Memory struct {
	items map[string]item
	locks map[string]memoryLock
	lock  sync.RWMutex
}

type item struct {
	val    interface{}
	expiry time.Time
}

type memoryLock struct {
	owner  string
	expiry time.Time
}

// NewMemory creates a new Memory driver
func NewMemory() *Memory {
	return &Memory{
		items: make(map[string]item),
		locks: make(map[string]memoryLock),
	}
}

func (c *Memory) Get(key string) interface{} {
	c.lock.RLock()
	it, ok := c.items[key]
	c.lock.RUnlock()
	if !ok {
		return nil
	}
	if !it.expiry.IsZero() && time.Now().After(it.expiry) {
		c.lock.Lock()
		// 二次确认后删除过期项，避免并发下误删新值。
		current, exists := c.items[key]
		if exists && current.expiry == it.expiry {
			delete(c.items, key)
		}
		c.lock.Unlock()
		return nil
	}
	return it.val
}

func (c *Memory) Set(key string, val interface{}, ttl time.Duration) {
	c.lock.Lock()
	defer c.lock.Unlock()

	expiry := time.Time{}
	if ttl > 0 {
		expiry = time.Now().Add(ttl)
	}

	c.items[key] = item{
		val:    val,
		expiry: expiry,
	}
}

func (c *Memory) Has(key string) bool {
	return c.Get(key) != nil
}

func (c *Memory) Delete(key string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	delete(c.items, key)
}

func (c *Memory) Clear() {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.items = make(map[string]item)
	c.locks = make(map[string]memoryLock)
}

func (c *Memory) Inc(key string, step int64) int64 {
	c.lock.Lock()
	defer c.lock.Unlock()

	it, ok := c.items[key]
	if ok && !it.expiry.IsZero() && time.Now().After(it.expiry) {
		// Inc/Dec 也必须遵守过期语义，避免基于旧值递增并继承已过期时间。
		delete(c.items, key)
		it = item{}
		ok = false
	}

	var val int64 = 0
	if ok {
		if v, ok := it.val.(int); ok {
			val = int64(v)
		} else if v, ok := it.val.(int64); ok {
			val = v
		} else if v, ok := it.val.(float64); ok {
			val = int64(v)
		}
	}

	val += step
	c.items[key] = item{
		val:    val,
		expiry: it.expiry,
	}
	return val
}

func (c *Memory) Dec(key string, step int64) int64 {
	return c.Inc(key, -step)
}

// AcquireLock 获取基于内存的进程内锁，并支持过期时间自动回收。
func (c *Memory) AcquireLock(key string, owner string, ttl time.Duration) bool {
	c.lock.Lock()
	defer c.lock.Unlock()

	if existing, ok := c.locks[key]; ok {
		if existing.expiry.After(time.Now()) {
			return false
		}
	}

	c.locks[key] = memoryLock{
		owner:  owner,
		expiry: time.Now().Add(ttl),
	}
	return true
}

// ReleaseLock 仅允许锁拥有者释放锁，避免并发误删。
func (c *Memory) ReleaseLock(key string, owner string) bool {
	c.lock.Lock()
	defer c.lock.Unlock()

	existing, ok := c.locks[key]
	if !ok || existing.owner != owner {
		return false
	}
	delete(c.locks, key)
	return true
}
