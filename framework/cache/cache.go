package cache

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"thinkgo/framework/debug"
)

const (
	defaultStoreName = "default"
	tagMetaPrefix    = "__thinkgo_tag__:"
	lockKeyPrefix    = "__thinkgo_lock__:"
	// defaultLockTTL 未显式指定时缓存锁的默认持有时长。
	defaultLockTTL = 10 * time.Second
)

// DistributedLocker 定义缓存驱动可选实现的分布式锁能力。
type DistributedLocker interface {
	AcquireLock(key string, owner string, ttl time.Duration) bool
	ReleaseLock(key string, owner string) bool
}

type cacheState struct {
	debug       *debug.Debug
	stores      map[string]Driver
	defaultName string
	storesMu    sync.RWMutex // 保护 stores 的并发注册与读取
	tagMu       sync.Mutex
	lockSeq     uint64
}

// Cache 是缓存管理器，同时承担 store/tag/lock 的统一入口。
type Cache struct {
	state     *cacheState
	storeName string
}

// TaggedCache 提供带标签的缓存读写能力。
type TaggedCache struct {
	cache *Cache
	tags  []string
}

// Lock 表示一个可获取和释放的缓存锁。
type Lock struct {
	cache *Cache
	key   string
	ttl   time.Duration
	owner string
}

// NewCache 创建新的缓存管理器。
func NewCache(debug *debug.Debug, driver Driver) *Cache {
	state := &cacheState{
		debug:       debug,
		stores:      make(map[string]Driver),
		defaultName: defaultStoreName,
	}
	if driver != nil {
		state.stores[defaultStoreName] = driver
	}
	return &Cache{
		state:     state,
		storeName: defaultStoreName,
	}
}

// RegisterStore 注册新的缓存 store。
func (c *Cache) RegisterStore(name string, driver Driver) *Cache {
	if c == nil || c.state == nil || name == "" || driver == nil {
		return c
	}
	c.state.storesMu.Lock()
	c.state.stores[name] = driver
	c.state.storesMu.Unlock()
	return c
}

// Store 切换到指定 store，未找到时回退到当前实例。
func (c *Cache) Store(name string) *Cache {
	if c == nil || c.state == nil || name == "" {
		return c
	}
	c.state.storesMu.RLock()
	_, ok := c.state.stores[name]
	c.state.storesMu.RUnlock()
	if !ok {
		return c
	}
	return &Cache{
		state:     c.state,
		storeName: name,
	}
}

// Tag 创建标签缓存视图。
func (c *Cache) Tag(tags ...string) *TaggedCache {
	filtered := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag != "" {
			filtered = append(filtered, tag)
		}
	}
	return &TaggedCache{
		cache: c,
		tags:  filtered,
	}
}

// Lock 创建缓存锁对象。
func (c *Cache) Lock(key string, ttl time.Duration) *Lock {
	if ttl <= 0 {
		ttl = defaultLockTTL
	}
	return &Lock{
		cache: c,
		key:   lockKeyPrefix + key,
		ttl:   ttl,
		owner: c.nextLockOwner(),
	}
}

// Get 获取缓存值。
func (c *Cache) Get(key string) interface{} {
	if c.debug() != nil {
		c.debug().AddCache("GET", key)
	}
	return c.driver().Get(key)
}

// Set 设置缓存值。
func (c *Cache) Set(key string, val interface{}, ttl time.Duration) {
	if c.debug() != nil {
		c.debug().AddCache("SET", key)
	}
	c.driver().Set(key, val, ttl)
}

// Has 判断键是否存在。
func (c *Cache) Has(key string) bool {
	if c.debug() != nil {
		c.debug().AddCache("HAS", key)
	}
	return c.driver().Has(key)
}

// Forget 删除指定键。
func (c *Cache) Forget(key string) {
	if c.debug() != nil {
		c.debug().AddCache("FORGET", key)
	}
	c.driver().Delete(key)
}

// Forever 永久写入缓存。
func (c *Cache) Forever(key string, val interface{}) {
	if c.debug() != nil {
		c.debug().AddCache("FOREVER", key)
	}
	c.driver().Set(key, val, 0)
}

// Flush 清空当前 store 的全部缓存。
func (c *Cache) Flush() {
	if c.debug() != nil {
		c.debug().AddCache("FLUSH", "*")
	}
	c.driver().Clear()
}

// Remember 获取缓存，不存在时通过回调写入。
func (c *Cache) Remember(key string, ttl time.Duration, callback func() interface{}) interface{} {
	val := c.Get(key)
	if val != nil {
		return val
	}

	val = callback()
	c.Set(key, val, ttl)
	return val
}

// Inc 原子递增缓存值。
func (c *Cache) Inc(key string, step int64) int64 {
	if c.debug() != nil {
		c.debug().AddCache("INC", key)
	}
	return c.driver().Inc(key, step)
}

// Dec 原子递减缓存值。
func (c *Cache) Dec(key string, step int64) int64 {
	if c.debug() != nil {
		c.debug().AddCache("DEC", key)
	}
	return c.driver().Dec(key, step)
}

// Get 读取带标签缓存中的指定键。
func (c *TaggedCache) Get(key string) interface{} {
	return c.cache.Get(key)
}

// Set 写入带标签缓存，并维护标签与键的映射。
func (c *TaggedCache) Set(key string, value interface{}, ttl time.Duration) {
	c.cache.Set(key, value, ttl)
	c.bindKeyToTags(key)
}

// Has 判断带标签缓存是否存在。
func (c *TaggedCache) Has(key string) bool {
	return c.cache.Has(key)
}

// Forget 删除带标签缓存，并同步移除标签映射。
func (c *TaggedCache) Forget(key string) {
	c.cache.Forget(key)
	c.unbindKeyFromTags(key)
}

// Flush 清理标签关联的全部缓存键。
func (c *TaggedCache) Flush() {
	if c == nil || c.cache == nil {
		return
	}

	c.cache.state.tagMu.Lock()
	defer c.cache.state.tagMu.Unlock()

	seen := make(map[string]bool)
	for _, tag := range c.tags {
		for _, key := range c.cache.tagMembers(tag) {
			if !seen[key] {
				c.cache.Forget(key)
				seen[key] = true
			}
		}
		c.cache.Forget(c.cache.tagMetaKey(tag))
	}
}

// Acquire 获取缓存锁。
func (l *Lock) Acquire() bool {
	if l == nil || l.cache == nil {
		return false
	}
	if locker, ok := l.cache.driver().(DistributedLocker); ok {
		return locker.AcquireLock(l.key, l.owner, l.ttl)
	}
	return false
}

// Release 释放缓存锁，仅锁拥有者可释放。
func (l *Lock) Release() bool {
	if l == nil || l.cache == nil {
		return false
	}
	if locker, ok := l.cache.driver().(DistributedLocker); ok {
		return locker.ReleaseLock(l.key, l.owner)
	}
	return false
}

func (c *Cache) driver() Driver {
	if c == nil || c.state == nil {
		return nil
	}
	c.state.storesMu.RLock()
	defer c.state.storesMu.RUnlock()
	if driver, ok := c.state.stores[c.storeName]; ok && driver != nil {
		return driver
	}
	if driver, ok := c.state.stores[c.state.defaultName]; ok {
		return driver
	}
	return nil
}

func (c *Cache) debug() *debug.Debug {
	if c == nil || c.state == nil {
		return nil
	}
	return c.state.debug
}

func (c *Cache) nextLockOwner() string {
	if c == nil || c.state == nil {
		return ""
	}
	sequence := atomic.AddUint64(&c.state.lockSeq, 1)
	return fmt.Sprintf("%s:%d:%d", c.storeName, time.Now().UnixNano(), sequence)
}

func (c *Cache) tagMetaKey(tag string) string {
	return tagMetaPrefix + c.storeName + ":" + tag
}

func (c *Cache) tagMembers(tag string) []string {
	raw := c.Get(c.tagMetaKey(tag))
	switch typed := raw.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func (c *TaggedCache) bindKeyToTags(key string) {
	if c == nil || c.cache == nil || len(c.tags) == 0 {
		return
	}

	c.cache.state.tagMu.Lock()
	defer c.cache.state.tagMu.Unlock()

	for _, tag := range c.tags {
		members := c.cache.tagMembers(tag)
		if containsString(members, key) {
			continue
		}
		members = append(members, key)
		c.cache.Set(c.cache.tagMetaKey(tag), members, 0)
	}
}

func (c *TaggedCache) unbindKeyFromTags(key string) {
	if c == nil || c.cache == nil || len(c.tags) == 0 {
		return
	}

	c.cache.state.tagMu.Lock()
	defer c.cache.state.tagMu.Unlock()

	for _, tag := range c.tags {
		members := c.cache.tagMembers(tag)
		filtered := make([]string, 0, len(members))
		for _, member := range members {
			if member != key {
				filtered = append(filtered, member)
			}
		}
		c.cache.Set(c.cache.tagMetaKey(tag), filtered, 0)
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
