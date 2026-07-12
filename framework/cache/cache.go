package cache

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"thinkgo/framework/debug"
)

const (
	defaultStoreName   = "default"
	tagMetaPrefix      = "__thinkgo_tag__:"
	tagReversePrefix   = "__thinkgo_tag_reverse__:"
	lockKeyPrefix      = "__thinkgo_lock__:"
	defaultLockTTL     = 10 * time.Second
	maxLockTTL         = 24 * time.Hour
	maxCacheTTL        = 100 * 365 * 24 * time.Hour
	maxCacheKeyBytes   = 1024
	maxCacheTagBytes   = 128
	maxTagMembers      = 10000
	maxTagsPerCacheKey = 64
)

var (
	cacheStorePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

	// ErrCacheDriverNotConfigured 表示当前 store 没有可用驱动。
	ErrCacheDriverNotConfigured = errors.New("缓存驱动未配置")
	// ErrCacheStoreNotFound 表示请求的 store 未注册。
	ErrCacheStoreNotFound = errors.New("缓存 store 不存在")
	// ErrCacheStoreExists 表示同名 store 已绑定到另一个驱动。
	ErrCacheStoreExists = errors.New("缓存 store 已存在")
	// ErrInvalidCacheStore 表示 store 名称或驱动无效。
	ErrInvalidCacheStore = errors.New("缓存 store 非法")
	// ErrInvalidCacheKey 表示缓存键为空、过长或包含控制字符。
	ErrInvalidCacheKey = errors.New("缓存键非法")
	// ErrReservedCacheKey 表示业务代码尝试使用框架内部命名空间。
	ErrReservedCacheKey = errors.New("缓存键使用了内部保留前缀")
	// ErrInvalidCacheTag 表示标签为空、过长、数量超限或包含非法字符。
	ErrInvalidCacheTag = errors.New("缓存标签非法")
	// ErrInvalidCacheTTL 表示缓存或锁的过期时间不在允许范围。
	ErrInvalidCacheTTL = errors.New("缓存过期时间非法")
	// ErrInvalidCounterStep 表示 Inc/Dec 使用了负步长。
	ErrInvalidCounterStep = errors.New("缓存计数步长非法")
	// ErrNilRememberCallback 表示 Remember 没有提供加载函数。
	ErrNilRememberCallback = errors.New("Remember 回调不能为空")
	// ErrCacheLockUnsupported 表示当前驱动不支持锁。
	ErrCacheLockUnsupported = errors.New("缓存驱动不支持锁")
	// ErrInvalidCacheLock 表示锁键或 owner 创建失败。
	ErrInvalidCacheLock = errors.New("缓存锁非法")
	// ErrCorruptTagMetadata 表示标签元数据类型或成员内容损坏。
	ErrCorruptTagMetadata = errors.New("缓存标签元数据损坏")
	// ErrCacheClosed 表示缓存管理器已经关闭。
	ErrCacheClosed = errors.New("缓存管理器已关闭")
)

// DistributedLocker 定义缓存驱动可选实现的分布式或进程级锁能力。
type DistributedLocker interface {
	AcquireLock(key string, owner string, ttl time.Duration) (bool, error)
	ReleaseLock(key string, owner string) (bool, error)
}

type rememberCall struct {
	done  chan struct{}
	value interface{}
	err   error
}

type cacheState struct {
	debug       *debug.Debug
	stores      map[string]Driver
	storesMu    sync.RWMutex
	operationMu sync.RWMutex
	tagMu       sync.RWMutex
	rememberMu  sync.Mutex
	remembering map[string]*rememberCall
	closeOnce   sync.Once
	closeErr    error
	closed      bool
}

// Cache 是缓存管理器，同时承担 store、标签、合并加载和锁的统一入口。
type Cache struct {
	state     *cacheState
	storeName string
}

// TaggedCache 提供带标签的缓存读写能力。
type TaggedCache struct {
	cache *Cache
	tags  []string
}

// Lock 表示一个有 owner 校验和 TTL 的缓存锁。
type Lock struct {
	cache    *Cache
	key      string
	ttl      time.Duration
	owner    string
	mutex    sync.Mutex
	acquired bool
}

// NewCache 创建缓存管理器；空驱动会在首次操作时返回明确错误。
func NewCache(trace *debug.Debug, driver Driver) *Cache {
	state := &cacheState{
		debug:       trace,
		stores:      make(map[string]Driver),
		remembering: make(map[string]*rememberCall),
	}
	if !isNilCacheDriver(driver) {
		state.stores[defaultStoreName] = driver
	}
	return &Cache{state: state, storeName: defaultStoreName}
}

// RegisterStore 注册或原子替换一个具名缓存 store。
func (c *Cache) RegisterStore(name string, driver Driver) error {
	if c == nil || c.state == nil {
		return ErrCacheDriverNotConfigured
	}
	if !cacheStorePattern.MatchString(name) || isNilCacheDriver(driver) {
		return fmt.Errorf("%w: %q", ErrInvalidCacheStore, name)
	}
	c.state.storesMu.Lock()
	defer c.state.storesMu.Unlock()
	if c.state.closed {
		return ErrCacheClosed
	}
	if existing, exists := c.state.stores[name]; exists {
		existingIdentity, existingOK := cacheDriverIdentity(existing)
		newIdentity, newOK := cacheDriverIdentity(driver)
		if existingOK && newOK && existingIdentity == newIdentity {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrCacheStoreExists, name)
	}
	c.state.stores[name] = driver
	return nil
}

// Store 返回指定 store 的不可变视图；未知名称不会静默回退。
func (c *Cache) Store(name string) (*Cache, error) {
	if c == nil || c.state == nil {
		return nil, ErrCacheDriverNotConfigured
	}
	if !cacheStorePattern.MatchString(name) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidCacheStore, name)
	}
	c.state.storesMu.RLock()
	defer c.state.storesMu.RUnlock()
	if c.state.closed {
		return nil, ErrCacheClosed
	}
	driver, exists := c.state.stores[name]
	if !exists || isNilCacheDriver(driver) {
		return nil, fmt.Errorf("%w: %s", ErrCacheStoreNotFound, name)
	}
	return &Cache{state: c.state, storeName: name}, nil
}

// Tag 创建去重后的标签缓存视图。
func (c *Cache) Tag(tags ...string) (*TaggedCache, error) {
	if c == nil || c.state == nil {
		return nil, ErrCacheDriverNotConfigured
	}
	if len(tags) == 0 {
		return nil, ErrInvalidCacheTag
	}
	filtered := make([]string, 0, len(tags))
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		if err := validateCacheTag(tag); err != nil {
			return nil, err
		}
		if !seen[tag] {
			seen[tag] = true
			filtered = append(filtered, tag)
		}
	}
	if len(filtered) > maxTagsPerCacheKey {
		return nil, fmt.Errorf("%w: 单键标签超过 %d", ErrInvalidCacheTag, maxTagsPerCacheKey)
	}
	_, release, err := c.driver()
	if err != nil {
		return nil, err
	}
	release()
	return &TaggedCache{cache: c, tags: filtered}, nil
}

// Lock 创建缓存锁；ttl 为 0 时使用默认值，负数和超长 TTL 会被拒绝。
func (c *Cache) Lock(key string, ttl time.Duration) (*Lock, error) {
	if err := validatePublicCacheKey(key); err != nil {
		return nil, err
	}
	if ttl == 0 {
		ttl = defaultLockTTL
	}
	if ttl < 0 || ttl > maxLockTTL {
		return nil, fmt.Errorf("%w: %s", ErrInvalidCacheTTL, ttl)
	}
	_, release, err := c.driver()
	if err != nil {
		return nil, err
	}
	release()
	owner, err := newLockOwner(c.storeName)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCacheLock, err)
	}
	return &Lock{cache: c, key: lockKeyPrefix + key, ttl: ttl, owner: owner}, nil
}

// Get 获取缓存值，并通过 found 区分未命中和命中的 nil。
func (c *Cache) Get(key string) (interface{}, bool, error) {
	if err := validatePublicCacheKey(key); err != nil {
		return nil, false, err
	}
	driver, release, err := c.driver()
	if err != nil {
		return nil, false, err
	}
	defer release()
	c.trace("GET", key)
	return driver.Get(key)
}

// Set 设置缓存值，ttl 为 0 表示永不过期。
func (c *Cache) Set(key string, value interface{}, ttl time.Duration) error {
	if err := validatePublicCacheKey(key); err != nil {
		return err
	}
	if err := validateCacheTTL(ttl); err != nil {
		return err
	}
	driver, release, err := c.driver()
	if err != nil {
		return err
	}
	defer release()
	c.trace("SET", key)
	c.state.tagMu.RLock()
	defer c.state.tagMu.RUnlock()
	return driver.Set(key, value, ttl)
}

// Has 判断未过期键是否存在。
func (c *Cache) Has(key string) (bool, error) {
	if err := validatePublicCacheKey(key); err != nil {
		return false, err
	}
	driver, release, err := c.driver()
	if err != nil {
		return false, err
	}
	defer release()
	c.trace("HAS", key)
	return driver.Has(key)
}

// Forget 删除指定键并清理它的全部标签反向关系。
func (c *Cache) Forget(key string) error {
	if err := validatePublicCacheKey(key); err != nil {
		return err
	}
	driver, release, err := c.driver()
	if err != nil {
		return err
	}
	defer release()
	c.trace("FORGET", key)
	c.state.tagMu.Lock()
	defer c.state.tagMu.Unlock()
	rollback, err := c.unbindKeyFromAllTagsLocked(driver, key)
	if err != nil {
		return err
	}
	if err = driver.Delete(key); err != nil {
		return errors.Join(err, rollback())
	}
	return nil
}

// Forever 永久写入缓存。
func (c *Cache) Forever(key string, value interface{}) error {
	return c.Set(key, value, 0)
}

// Flush 清空当前 store 的数据和标签元数据，但保留锁。
func (c *Cache) Flush() error {
	driver, release, err := c.driver()
	if err != nil {
		return err
	}
	defer release()
	c.trace("FLUSH", "*")
	c.state.tagMu.Lock()
	defer c.state.tagMu.Unlock()
	return driver.Clear()
}

// Remember 合并同进程内同 store、同 key 的并发未命中加载。
func (c *Cache) Remember(key string, ttl time.Duration, callback func() (interface{}, error)) (interface{}, error) {
	if callback == nil {
		return nil, ErrNilRememberCallback
	}
	if err := validatePublicCacheKey(key); err != nil {
		return nil, err
	}
	if err := validateCacheTTL(ttl); err != nil {
		return nil, err
	}
	if value, found, err := c.Get(key); err != nil || found {
		return value, err
	}
	callKey := c.storeName + "\x00" + key
	return c.state.doRemember(callKey, func() (interface{}, error) {
		if value, found, err := c.Get(key); err != nil || found {
			return value, err
		}
		value, err := callback()
		if err != nil {
			return nil, err
		}
		if err = c.Set(key, value, ttl); err != nil {
			return nil, err
		}
		return value, nil
	})
}

// Inc 严格递增整数缓存值。
func (c *Cache) Inc(key string, step int64) (int64, error) {
	if step < 0 {
		return 0, fmt.Errorf("%w: %d", ErrInvalidCounterStep, step)
	}
	if err := validatePublicCacheKey(key); err != nil {
		return 0, err
	}
	driver, release, err := c.driver()
	if err != nil {
		return 0, err
	}
	defer release()
	c.trace("INC", key)
	c.state.tagMu.RLock()
	defer c.state.tagMu.RUnlock()
	return driver.Inc(key, step)
}

// Dec 严格递减整数缓存值。
func (c *Cache) Dec(key string, step int64) (int64, error) {
	if step < 0 {
		return 0, fmt.Errorf("%w: %d", ErrInvalidCounterStep, step)
	}
	if err := validatePublicCacheKey(key); err != nil {
		return 0, err
	}
	driver, release, err := c.driver()
	if err != nil {
		return 0, err
	}
	defer release()
	c.trace("DEC", key)
	c.state.tagMu.RLock()
	defer c.state.tagMu.RUnlock()
	return driver.Dec(key, step)
}

// Close 幂等关闭所有唯一的可关闭驱动，并拒绝后续操作。
func (c *Cache) Close() error {
	if c == nil || c.state == nil {
		return nil
	}
	c.state.closeOnce.Do(func() {
		c.state.operationMu.Lock()
		c.state.storesMu.Lock()
		c.state.closed = true
		drivers := make([]Driver, 0, len(c.state.stores))
		seen := make(map[string]bool)
		for _, driver := range c.state.stores {
			identity, identifiable := cacheDriverIdentity(driver)
			if identifiable && seen[identity] {
				continue
			}
			if identifiable {
				seen[identity] = true
			}
			drivers = append(drivers, driver)
		}
		c.state.storesMu.Unlock()
		// closed 已阻止新操作；释放生命周期锁后再执行外部 Close，避免驱动回调造成死锁。
		c.state.operationMu.Unlock()

		closeErrors := make([]error, 0)
		for _, driver := range drivers {
			if closer, ok := driver.(interface{ Close() error }); ok {
				closeErrors = append(closeErrors, closer.Close())
			}
		}
		c.state.closeErr = errors.Join(closeErrors...)
	})
	return c.state.closeErr
}

// Get 读取带标签缓存中的指定键。
func (c *TaggedCache) Get(key string) (interface{}, bool, error) {
	if c == nil || c.cache == nil {
		return nil, false, ErrCacheDriverNotConfigured
	}
	value, found, err := c.cache.Get(key)
	if err != nil || found {
		return value, found, err
	}
	// 自然过期后及时移除标签元数据，避免成员列表只增不减。
	if err = c.cache.Forget(key); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

// Set 写入缓存并维护标签与键的双向元数据。
func (c *TaggedCache) Set(key string, value interface{}, ttl time.Duration) error {
	if c == nil || c.cache == nil {
		return ErrCacheDriverNotConfigured
	}
	if err := validatePublicCacheKey(key); err != nil {
		return err
	}
	if err := validateCacheTTL(ttl); err != nil {
		return err
	}
	driver, release, err := c.cache.driver()
	if err != nil {
		return err
	}
	defer release()
	c.cache.trace("TAG_SET", key)
	c.cache.state.tagMu.Lock()
	defer c.cache.state.tagMu.Unlock()
	rollback, err := c.bindKeyToTagsLocked(driver, key)
	if err != nil {
		return err
	}
	if err = driver.Set(key, value, ttl); err != nil {
		return errors.Join(err, rollback())
	}
	return nil
}

// Has 判断带标签缓存是否存在。
func (c *TaggedCache) Has(key string) (bool, error) {
	if c == nil || c.cache == nil {
		return false, ErrCacheDriverNotConfigured
	}
	found, err := c.cache.Has(key)
	if err != nil || found {
		return found, err
	}
	if err = c.cache.Forget(key); err != nil {
		return false, err
	}
	return false, nil
}

// Forget 删除带标签缓存，并同步移除全部标签映射。
func (c *TaggedCache) Forget(key string) error {
	if c == nil || c.cache == nil {
		return ErrCacheDriverNotConfigured
	}
	return c.cache.Forget(key)
}

// Flush 清理标签关联的全部缓存键及其反向元数据。
func (c *TaggedCache) Flush() error {
	if c == nil || c.cache == nil {
		return ErrCacheDriverNotConfigured
	}
	driver, release, err := c.cache.driver()
	if err != nil {
		return err
	}
	defer release()
	c.cache.trace("TAG_FLUSH", strings.Join(c.tags, ","))
	c.cache.state.tagMu.Lock()
	defer c.cache.state.tagMu.Unlock()

	keys := make(map[string][]string)
	for _, tag := range c.tags {
		members, memberErr := c.cache.tagMembersLocked(driver, tag)
		if memberErr != nil {
			return memberErr
		}
		for _, key := range members {
			keys[key] = append(keys[key], tag)
		}
	}
	// 变更前验证双向关系，防止损坏元数据诱导删除任意业务键。
	for key, requiredTags := range keys {
		reverse, reverseErr := c.cache.reverseTagsLocked(driver, key)
		if reverseErr != nil {
			return reverseErr
		}
		for _, tag := range requiredTags {
			if !containsString(reverse, tag) {
				return fmt.Errorf("%w: 标签 %q 缺少键 %q 的反向关系", ErrCorruptTagMetadata, tag, key)
			}
		}
	}
	resultErr := error(nil)
	for key := range keys {
		rollback, unbindErr := c.cache.unbindKeyFromAllTagsLocked(driver, key)
		if unbindErr != nil {
			resultErr = errors.Join(resultErr, unbindErr)
			continue
		}
		if deleteErr := driver.Delete(key); deleteErr != nil {
			resultErr = errors.Join(resultErr, deleteErr, rollback())
		}
	}
	return resultErr
}

// Acquire 获取缓存锁；同一 Lock 实例不会重复获取。
func (l *Lock) Acquire() (bool, error) {
	if l == nil || l.cache == nil {
		return false, ErrInvalidCacheLock
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if l.acquired {
		return false, nil
	}
	driver, release, err := l.cache.driver()
	if err != nil {
		return false, err
	}
	defer release()
	locker, ok := driver.(DistributedLocker)
	if !ok {
		return false, ErrCacheLockUnsupported
	}
	acquired, err := locker.AcquireLock(l.key, l.owner, l.ttl)
	if err == nil && acquired {
		l.acquired = true
	}
	return acquired, err
}

// Release 仅由成功获取锁的本地实例释放对应 owner。
func (l *Lock) Release() (bool, error) {
	if l == nil || l.cache == nil {
		return false, ErrInvalidCacheLock
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if !l.acquired {
		return false, nil
	}
	driver, release, err := l.cache.driver()
	if err != nil {
		return false, err
	}
	defer release()
	locker, ok := driver.(DistributedLocker)
	if !ok {
		return false, ErrCacheLockUnsupported
	}
	released, err := locker.ReleaseLock(l.key, l.owner)
	if err == nil {
		l.acquired = false
	}
	return released, err
}

func (c *Cache) driver() (Driver, func(), error) {
	if c == nil || c.state == nil {
		return nil, nil, ErrCacheDriverNotConfigured
	}
	c.state.operationMu.RLock()
	c.state.storesMu.RLock()
	defer c.state.storesMu.RUnlock()
	if c.state.closed {
		c.state.operationMu.RUnlock()
		return nil, nil, ErrCacheClosed
	}
	driver, exists := c.state.stores[c.storeName]
	if !exists || isNilCacheDriver(driver) {
		c.state.operationMu.RUnlock()
		return nil, nil, fmt.Errorf("%w: %s", ErrCacheDriverNotConfigured, c.storeName)
	}
	return driver, c.state.operationMu.RUnlock, nil
}

func (c *Cache) trace(action, key string) {
	if c != nil && c.state != nil && c.state.debug != nil {
		c.state.debug.AddCache(action, key)
	}
}

func (s *cacheState) doRemember(key string, callback func() (interface{}, error)) (value interface{}, err error) {
	s.rememberMu.Lock()
	if existing := s.remembering[key]; existing != nil {
		s.rememberMu.Unlock()
		<-existing.done
		return existing.value, existing.err
	}
	call := &rememberCall{done: make(chan struct{})}
	s.remembering[key] = call
	s.rememberMu.Unlock()

	defer func() {
		if recovered := recover(); recovered != nil {
			s.finishRemember(key, call, nil, errors.New("Remember 回调发生 panic"))
			panic(recovered)
		}
	}()
	value, err = callback()
	s.finishRemember(key, call, value, err)
	return value, err
}

func (s *cacheState) finishRemember(key string, call *rememberCall, value interface{}, err error) {
	s.rememberMu.Lock()
	call.value = value
	call.err = err
	delete(s.remembering, key)
	close(call.done)
	s.rememberMu.Unlock()
}

func (c *TaggedCache) bindKeyToTagsLocked(driver Driver, key string) (func() error, error) {
	reverse, err := c.cache.reverseTagsLocked(driver, key)
	if err != nil {
		return nil, err
	}
	finalReverse := append([]string(nil), reverse...)
	for _, tag := range c.tags {
		if !containsString(finalReverse, tag) {
			finalReverse = append(finalReverse, tag)
		}
	}
	if len(finalReverse) > maxTagsPerCacheKey {
		return nil, fmt.Errorf("%w: 单键标签超过 %d", ErrInvalidCacheTag, maxTagsPerCacheKey)
	}

	updates := make([]metadataUpdate, 0, len(c.tags)+1)
	checkedTags := make(map[string]bool, len(finalReverse))
	for _, tag := range finalReverse {
		members, memberErr := c.cache.tagMembersLocked(driver, tag)
		if memberErr != nil {
			return nil, memberErr
		}
		containsKey := containsString(members, key)
		wasReverseTag := containsString(reverse, tag)
		if wasReverseTag && !containsKey {
			return nil, fmt.Errorf("%w: 标签 %q 缺少键 %q", ErrCorruptTagMetadata, tag, key)
		}
		if containsString(c.tags, tag) && !containsKey {
			if len(members) >= maxTagMembers {
				return nil, fmt.Errorf("%w: %s 成员超过 %d", ErrInvalidCacheTag, tag, maxTagMembers)
			}
			updates = append(updates, metadataUpdate{
				key:    c.cache.tagMetaKey(tag),
				before: cloneStrings(members),
				after:  append(cloneStrings(members), key),
			})
		}
		checkedTags[tag] = true
	}
	if len(checkedTags) != len(finalReverse) {
		return nil, ErrCorruptTagMetadata
	}
	if len(finalReverse) != len(reverse) {
		updates = append(updates, metadataUpdate{
			key:    c.cache.tagReverseKey(key),
			before: cloneStrings(reverse),
			after:  cloneStrings(finalReverse),
		})
	}
	return applyMetadataUpdates(driver, updates)
}

func (c *Cache) unbindKeyFromAllTagsLocked(driver Driver, key string) (func() error, error) {
	tags, err := c.reverseTagsLocked(driver, key)
	if err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return noopMetadataRollback, nil
	}
	updates := make([]metadataUpdate, 0, len(tags)+1)
	for _, tag := range tags {
		members, memberErr := c.tagMembersLocked(driver, tag)
		if memberErr != nil {
			return nil, memberErr
		}
		if !containsString(members, key) {
			return nil, fmt.Errorf("%w: 标签 %q 缺少键 %q", ErrCorruptTagMetadata, tag, key)
		}
		filtered := removeString(members, key)
		updates = append(updates, metadataUpdate{
			key:    c.tagMetaKey(tag),
			before: cloneStrings(members),
			after:  cloneStrings(filtered),
		})
	}
	updates = append(updates, metadataUpdate{
		key:    c.tagReverseKey(key),
		before: cloneStrings(tags),
		after:  nil,
	})
	return applyMetadataUpdates(driver, updates)
}

func (c *Cache) tagMembersLocked(driver Driver, tag string) ([]string, error) {
	return readStringMetadata(driver, c.tagMetaKey(tag), maxTagMembers, validatePublicCacheKey)
}

func (c *Cache) reverseTagsLocked(driver Driver, key string) ([]string, error) {
	return readStringMetadata(driver, c.tagReverseKey(key), maxTagsPerCacheKey, validateCacheTag)
}

func readStringMetadata(driver Driver, key string, limit int, validator func(string) error) ([]string, error) {
	raw, found, err := driver.Get(key)
	if err != nil || !found {
		return nil, err
	}
	result := make([]string, 0)
	switch typed := raw.(type) {
	case []string:
		result = append(result, typed...)
	case []interface{}:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, ErrCorruptTagMetadata
			}
			result = append(result, text)
		}
	default:
		return nil, ErrCorruptTagMetadata
	}
	if len(result) > limit {
		return nil, ErrCorruptTagMetadata
	}
	if len(result) == 0 {
		return nil, ErrCorruptTagMetadata
	}
	seen := make(map[string]bool, len(result))
	filtered := make([]string, 0, len(result))
	for _, value := range result {
		if value == "" || seen[value] || validator(value) != nil {
			return nil, ErrCorruptTagMetadata
		}
		seen[value] = true
		filtered = append(filtered, value)
	}
	return filtered, nil
}

type metadataUpdate struct {
	key    string
	before []string
	after  []string
}

// applyMetadataUpdates 顺序提交元数据，并在任何错误时反向恢复所有可能已写入的键。
func applyMetadataUpdates(driver Driver, updates []metadataUpdate) (func() error, error) {
	for index, update := range updates {
		if err := writeStringMetadata(driver, update.key, update.after); err != nil {
			rollback := metadataRollback(driver, updates[:index+1])
			return nil, errors.Join(err, rollback())
		}
	}
	return metadataRollback(driver, updates), nil
}

func metadataRollback(driver Driver, updates []metadataUpdate) func() error {
	return func() error {
		var resultErr error
		for index := len(updates) - 1; index >= 0; index-- {
			resultErr = errors.Join(resultErr, writeStringMetadata(driver, updates[index].key, updates[index].before))
		}
		return resultErr
	}
}

func writeStringMetadata(driver Driver, key string, values []string) error {
	if len(values) == 0 {
		return driver.Delete(key)
	}
	return driver.Set(key, values, 0)
}

func noopMetadataRollback() error {
	return nil
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func (c *Cache) tagMetaKey(tag string) string {
	return tagMetaPrefix + c.storeName + ":" + tag
}

func (c *Cache) tagReverseKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return tagReversePrefix + c.storeName + ":" + hex.EncodeToString(digest[:])
}

func validatePublicCacheKey(key string) error {
	if key == "" || len(key) > maxCacheKeyBytes || !utf8.ValidString(key) || hasControlCharacter(key) {
		return fmt.Errorf("%w: %q", ErrInvalidCacheKey, key)
	}
	if strings.HasPrefix(key, tagMetaPrefix) || strings.HasPrefix(key, tagReversePrefix) || strings.HasPrefix(key, lockKeyPrefix) {
		return fmt.Errorf("%w: %q", ErrReservedCacheKey, key)
	}
	return nil
}

func validateCacheTag(tag string) error {
	if tag == "" || len(tag) > maxCacheTagBytes || !utf8.ValidString(tag) || hasControlCharacter(tag) || strings.TrimSpace(tag) != tag {
		return fmt.Errorf("%w: %q", ErrInvalidCacheTag, tag)
	}
	return nil
}

func validateCacheTTL(ttl time.Duration) error {
	if ttl < 0 || ttl > maxCacheTTL {
		return fmt.Errorf("%w: %s", ErrInvalidCacheTTL, ttl)
	}
	return nil
}

func hasControlCharacter(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func newLockOwner(storeName string) (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return storeName + ":" + hex.EncodeToString(random), nil
}

func isNilCacheDriver(driver Driver) bool {
	if driver == nil {
		return true
	}
	value := reflect.ValueOf(driver)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func cacheDriverIdentity(driver Driver) (string, bool) {
	if isNilCacheDriver(driver) {
		return "", false
	}
	value := reflect.ValueOf(driver)
	if value.Kind() == reflect.Pointer {
		return fmt.Sprintf("%T:%x", driver, value.Pointer()), true
	}
	return "", false
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func removeString(items []string, target string) []string {
	filtered := make([]string, 0, len(items))
	for _, item := range items {
		if item != target {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
