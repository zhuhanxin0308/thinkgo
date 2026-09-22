package cache

import (
	"context"
	"crypto/rand"
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

	"github.com/zhuhanxin0308/thinkgo/framework/debug"
)

const (
	defaultStoreName     = "default"
	tagMetaPrefix        = "__thinkgo_tag__:"
	tagReversePrefix     = "__thinkgo_tag_reverse__:"
	cacheFencePrefix     = "__thinkgo_cache_fence__:"
	lockKeyPrefix        = "__thinkgo_lock__:"
	defaultLockTTL       = 10 * time.Second
	maxLockTTL           = 24 * time.Hour
	tagMutationLockTTL   = 30 * time.Second
	tagMutationWait      = 200 * time.Millisecond
	rememberLockWait     = 5 * time.Second
	lockRetryInterval    = 5 * time.Millisecond
	maxCacheTTL          = 100 * 365 * 24 * time.Hour
	maxCacheKeyBytes     = 1024
	maxCacheTagBytes     = 128
	maxTagMembers        = 10000
	maxTagsPerCacheKey   = 64
	maxCacheBatchEntries = 10000
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
	// ErrCacheLockBusy 表示在限定等待窗口内未能获得缓存锁。
	ErrCacheLockBusy = errors.New("缓存锁忙")
	// ErrCacheLockLost 表示临界区结束时锁已经过期或被替换。
	ErrCacheLockLost = errors.New("缓存锁已丢失")
	// ErrInvalidCacheLock 表示锁键或 owner 创建失败。
	ErrInvalidCacheLock = errors.New("缓存锁非法")
	// ErrCorruptTagMetadata 表示标签元数据类型或成员内容损坏。
	ErrCorruptTagMetadata = errors.New("缓存标签元数据损坏")
	// ErrCacheClosed 表示缓存管理器已经关闭。
	ErrCacheClosed = errors.New("缓存管理器已关闭")
	// ErrCacheBatchTooLarge 表示批量缓存操作超过了单次资源预算。
	ErrCacheBatchTooLarge = errors.New("缓存批量操作过大")
	// ErrInvalidCacheContext 表示缓存上下文为空。
	ErrInvalidCacheContext = errors.New("缓存上下文无效")
)

// DistributedLocker 定义缓存驱动可选实现的分布式或进程级锁能力。
type DistributedLocker interface {
	AcquireLock(key string, owner string, ttl time.Duration) (bool, error)
	ReleaseLock(key string, owner string) (bool, error)
}

// LockRenewer 定义驱动可选的锁租约续期能力。
type LockRenewer interface {
	RenewLock(key string, owner string, ttl time.Duration) (bool, error)
}

type cacheResourceIdentified interface {
	CacheResourceIdentity() string
}

type rememberCall struct {
	done  chan struct{}
	value interface{}
	err   error
}

type cacheState struct {
	stores       map[string]Driver
	storeOptions map[string]StoreOptions
	storesMu     sync.RWMutex
	operationMu  sync.RWMutex
	tagMu        sync.RWMutex
	rememberMu   sync.Mutex
	remembering  map[string]*rememberCall
	closeOnce    sync.Once
	closeErr     error
	closed       bool
}

// Cache 是缓存管理器，同时承担 store、标签、合并加载和锁的统一入口。
type Cache struct {
	state     *cacheState
	storeName string
	debug     *debug.Debug
}

// StoreOptions 定义 ThinkPHP 缓存 store 的键前缀、默认有效期和标签前缀。
type StoreOptions struct {
	Prefix    string
	Expire    time.Duration
	TagPrefix string
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
// trace 只绑定返回的兼容 facade，不进入共享 state；请求代码应优先使用 WithDebug 派生私有 facade。
func NewCache(trace *debug.Debug, driver Driver) *Cache {
	state := &cacheState{
		stores:       make(map[string]Driver),
		storeOptions: make(map[string]StoreOptions),
		remembering:  make(map[string]*rememberCall),
	}
	if !isNilCacheDriver(driver) {
		state.stores[defaultStoreName] = driver
		state.storeOptions[defaultStoreName] = StoreOptions{TagPrefix: "tag:"}
	}
	return &Cache{state: state, storeName: defaultStoreName, debug: trace}
}

// WithDebug 返回绑定指定请求 collector 的轻量 facade，并继续共享驱动、Store 与生命周期状态。
// Cache facade 在构造后应视为不可变对象，可安全地由单个请求及其派生 Store、Tag 使用。
func (c *Cache) WithDebug(collector *debug.Debug) *Cache {
	if c == nil {
		return nil
	}
	if c.debug == collector {
		return c
	}
	requestCache := *c
	requestCache.debug = collector
	return &requestCache
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
		if sameCacheDriver(existing, driver) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrCacheStoreExists, name)
	}
	newResource, newResourceOK := cacheDriverResourceIdentity(driver)
	if newResourceOK {
		for existingName, existingDriver := range c.state.stores {
			if sameCacheDriver(existingDriver, driver) {
				continue
			}
			existingResource, existingResourceOK := cacheDriverResourceIdentity(existingDriver)
			if existingResourceOK && existingResource == newResource {
				return fmt.Errorf("%w: %s 与 %s 共享后端 namespace", ErrCacheStoreExists, existingName, name)
			}
		}
	}
	c.state.stores[name] = driver
	if c.state.storeOptions == nil {
		c.state.storeOptions = make(map[string]StoreOptions)
	}
	if _, exists := c.state.storeOptions[name]; !exists {
		c.state.storeOptions[name] = StoreOptions{TagPrefix: "tag:"}
	}
	return nil
}

// ConfigureStore 应用 ThinkPHP cache.stores 中的公共选项。
func (c *Cache) ConfigureStore(name string, options StoreOptions) error {
	if c == nil || c.state == nil {
		return ErrCacheDriverNotConfigured
	}
	if !cacheStorePattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidCacheStore, name)
	}
	if options.Expire < 0 || options.Expire > maxCacheTTL {
		return fmt.Errorf("%w: %s", ErrInvalidCacheTTL, options.Expire)
	}
	if len(options.Prefix) > maxCacheKeyBytes || !utf8.ValidString(options.Prefix) || hasControlCharacter(options.Prefix) {
		return fmt.Errorf("%w: store 前缀非法", ErrInvalidCacheKey)
	}
	if options.TagPrefix == "" {
		options.TagPrefix = "tag:"
	}
	if len(options.TagPrefix) > maxCacheTagBytes || !utf8.ValidString(options.TagPrefix) || hasControlCharacter(options.TagPrefix) {
		return fmt.Errorf("%w: store 标签前缀非法", ErrInvalidCacheTag)
	}
	c.state.storesMu.Lock()
	defer c.state.storesMu.Unlock()
	if c.state.closed {
		return ErrCacheClosed
	}
	if _, exists := c.state.stores[name]; !exists {
		return fmt.Errorf("%w: %s", ErrCacheStoreNotFound, name)
	}
	if c.state.storeOptions == nil {
		c.state.storeOptions = make(map[string]StoreOptions)
	}
	c.state.storeOptions[name] = options
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
	return &Cache{state: c.state, storeName: name, debug: c.debug}, nil
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
	return c.GetContext(context.Background(), key)
}

// GetContext 读取缓存并在支持时把调用方上下文传递给驱动。
func (c *Cache) GetContext(ctx context.Context, key string) (interface{}, bool, error) {
	if ctx == nil {
		return nil, false, ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := validatePublicCacheKey(key); err != nil {
		return nil, false, err
	}
	driver, release, err := c.driver()
	if err != nil {
		return nil, false, err
	}
	defer release()
	c.trace("GET", key)
	return c.getCacheValueAndRepairContext(ctx, driver, key)
}

// Set 设置缓存值；省略 ttl 时使用 store 的 expire，显式 0 表示永不过期。
func (c *Cache) Set(key string, value interface{}, ttl ...time.Duration) error {
	return c.SetContext(context.Background(), key, value, ttl...)
}

// SetContext 写入缓存并在支持时把调用方上下文传递给驱动。
func (c *Cache) SetContext(ctx context.Context, key string, value interface{}, ttl ...time.Duration) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validatePublicCacheKey(key); err != nil {
		return err
	}
	resolvedTTL, err := c.resolveTTL(ttl)
	if err != nil {
		return err
	}
	driver, release, err := c.driver()
	if err != nil {
		return err
	}
	defer release()
	c.trace("SET", key)
	return c.setCacheValueWithFenceContext(ctx, driver, key, value, resolvedTTL)
}

// Has 判断未过期键是否存在。
func (c *Cache) Has(key string) (bool, error) {
	return c.HasContext(context.Background(), key)
}

// HasContext 检查缓存键，并在驱动支持时透传请求上下文。
func (c *Cache) HasContext(ctx context.Context, key string) (bool, error) {
	if ctx == nil {
		return false, ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := validatePublicCacheKey(key); err != nil {
		return false, err
	}
	driver, release, err := c.driver()
	if err != nil {
		return false, err
	}
	defer release()
	c.trace("HAS", key)
	return c.hasCacheValueAndRepairContext(ctx, driver, key)
}

// Forget 删除指定键并清理它的全部标签反向关系。
func (c *Cache) Forget(key string) error {
	return c.ForgetContext(context.Background(), key)
}

// ForgetContext 删除缓存键及标签关系，并让支持上下文的驱动响应请求取消。
func (c *Cache) ForgetContext(ctx context.Context, key string) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validatePublicCacheKey(key); err != nil {
		return err
	}
	driver, release, err := c.driver()
	if err != nil {
		return err
	}
	defer release()
	c.trace("FORGET", key)
	return c.withTagMutationLockContext(ctx, driver, func() error {
		c.state.tagMu.Lock()
		defer c.state.tagMu.Unlock()
		if cacheDriverSupportsAtomicFencing(driver) {
			fence, fenceErr := c.beginKeyInvalidation(ctx, driver, []string{key})
			if fenceErr != nil {
				return fenceErr
			}
			_, found, readErr := getCacheValueContext(ctx, driver, key)
			if readErr != nil {
				return readErr
			}
			if found {
				deleted, deleteErr := c.deleteAtFenceContext(ctx, driver, key, fence)
				if deleteErr != nil || !deleted {
					return deleteErr
				}
			}
			_, cleanupErr := c.unbindKeyFromAllTagsLockedContext(ctx, driver, key)
			return cleanupErr
		}
		rollback, err := c.unbindKeyFromAllTagsLockedContext(ctx, driver, key)
		if err != nil {
			return err
		}
		if err = deleteCacheValueContext(ctx, driver, key); err != nil {
			return errors.Join(err, rollback())
		}
		return nil
	})
}

// Forever 永久写入缓存。
func (c *Cache) Forever(key string, value interface{}) error {
	return c.Set(key, value, 0)
}

// Flush 清空当前 store 的数据和标签元数据，但保留锁。
func (c *Cache) Flush() error {
	return c.FlushContext(context.Background())
}

// FlushContext 清空当前 store，并在支持时透传请求上下文。
func (c *Cache) FlushContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	driver, release, err := c.driver()
	if err != nil {
		return err
	}
	defer release()
	c.trace("FLUSH", "*")
	return c.withTagMutationLockContext(ctx, driver, func() error {
		c.state.tagMu.Lock()
		defer c.state.tagMu.Unlock()
		return c.clearAtFence(ctx, driver, "")
	})
}

// Remember 合并同进程内同 store、同 key 的并发未命中加载。
func (c *Cache) Remember(key string, ttl time.Duration, callback func() (interface{}, error)) (interface{}, error) {
	return c.RememberContext(context.Background(), key, ttl, callback)
}

// RememberContext 合并并发加载，并允许等待共享加载结果的调用方响应取消。
func (c *Cache) RememberContext(ctx context.Context, key string, ttl time.Duration, callback func() (interface{}, error)) (interface{}, error) {
	if ctx == nil {
		return nil, ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if callback == nil {
		return nil, ErrNilRememberCallback
	}
	if err := validatePublicCacheKey(key); err != nil {
		return nil, err
	}
	if err := validateCacheTTL(ttl); err != nil {
		return nil, err
	}
	if value, found, err := c.GetContext(ctx, key); err != nil || found {
		return value, err
	}
	callKey := c.storeName + "\x00" + key
	return c.state.doRememberContext(ctx, callKey, func() (interface{}, error) {
		if value, found, err := c.GetContext(ctx, key); err != nil || found {
			return value, err
		}
		value, err := callback()
		if err != nil {
			return nil, err
		}
		if err = c.SetContext(ctx, key, value, ttl); err != nil {
			return nil, err
		}
		return value, nil
	})
}

// RememberWithLock 在缓存未命中时使用驱动锁跨管理器合并加载；不支持锁的驱动会明确失败。
// 驱动必须同时支持 DistributedLocker、LockRenewer 与 LockGuardedUpdater，才能原子校验 owner 并写回。
// 不具备安全提交能力的自定义驱动在执行加载回调前返回 ErrCacheLockUnsupported。
func (c *Cache) RememberWithLock(key string, ttl, lockTTL time.Duration, callback func() (interface{}, error)) (value interface{}, resultErr error) {
	return c.RememberWithLockContext(context.Background(), key, ttl, lockTTL, callback)
}

// RememberWithLockContext 在缓存未命中时支持可取消的分布式锁等待与写回。
func (c *Cache) RememberWithLockContext(ctx context.Context, key string, ttl, lockTTL time.Duration, callback func() (interface{}, error)) (value interface{}, resultErr error) {
	if ctx == nil {
		return nil, ErrInvalidCacheContext
	}
	if callback == nil {
		return nil, ErrNilRememberCallback
	}
	if err := validatePublicCacheKey(key); err != nil {
		return nil, err
	}
	if err := validateCacheTTL(ttl); err != nil {
		return nil, err
	}
	if value, found, err := c.GetContext(ctx, key); err != nil || found {
		return value, err
	}
	driver, releaseDriver, driverErr := c.driver()
	if driverErr != nil {
		return nil, driverErr
	}
	_, lockerSupported := driver.(DistributedLocker)
	_, renewerSupported := driver.(LockRenewer)
	guardedSupported := cacheDriverSupportsGuardedUpdates(driver)
	releaseDriver()
	if !lockerSupported || !renewerSupported || !guardedSupported {
		return nil, ErrCacheLockUnsupported
	}
	lock, err := c.Lock(key, lockTTL)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(rememberLockWait)
	for {
		acquired, acquireErr := lock.AcquireContext(ctx)
		if acquireErr != nil {
			return nil, acquireErr
		}
		if acquired {
			break
		}
		if !time.Now().Before(deadline) {
			return nil, ErrCacheLockBusy
		}
		timer := time.NewTimer(lockRetryInterval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		}
	}
	defer func() {
		released, releaseErr := lock.ReleaseContext(context.Background())
		if releaseErr == nil && !released {
			releaseErr = ErrCacheLockLost
		}
		resultErr = errors.Join(resultErr, releaseErr)
	}()
	renewTTL := lock.ttl
	renewal := startLockRenewal(func() (bool, error) {
		return lock.Renew(renewTTL)
	}, renewTTL)
	defer func() {
		resultErr = errors.Join(resultErr, renewal.stop())
	}()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if value, found, err := c.GetContext(ctx, key); err != nil || found {
		return value, err
	}
	value, resultErr = callback()
	if resultErr != nil {
		return nil, resultErr
	}
	if err := ctx.Err(); err != nil {
		return value, err
	}
	if renewed, renewErr := lock.RenewContext(ctx, renewTTL); renewErr != nil || !renewed {
		if renewErr != nil {
			resultErr = renewErr
		} else {
			resultErr = ErrCacheLockLost
		}
		return value, resultErr
	}
	if resultErr = c.commitRememberValue(ctx, key, value, ttl, lock); resultErr != nil {
		return nil, resultErr
	}
	return value, nil
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
	return c.changeCounterWithFence(driver, key, step, false)
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
	return c.changeCounterWithFence(driver, key, step, true)
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

// Acquire 获取缓存锁；同一 Lock 实例不会重复获取。
func (l *Lock) Acquire() (bool, error) {
	return l.AcquireContext(context.Background())
}

// AcquireContext 获取缓存锁并在支持时把调用方上下文传递给驱动。
func (l *Lock) AcquireContext(ctx context.Context) (bool, error) {
	if ctx == nil {
		return false, ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
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
	if contextual, ok := driver.(ContextualDistributedLocker); ok {
		acquired, err := contextual.AcquireLockContext(ctx, l.key, l.owner, l.ttl)
		if err == nil && acquired {
			l.acquired = true
		}
		return acquired, err
	}
	acquired, err := locker.AcquireLock(l.key, l.owner, l.ttl)
	if err == nil && acquired {
		l.acquired = true
	}
	return acquired, err
}

// Release 仅由成功获取锁的本地实例释放对应 owner。
func (l *Lock) Release() (bool, error) {
	return l.ReleaseContext(context.Background())
}

// ReleaseContext 释放缓存锁并在支持时把调用方上下文传递给驱动。
func (l *Lock) ReleaseContext(ctx context.Context) (bool, error) {
	if ctx == nil {
		return false, ErrInvalidCacheContext
	}
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
	if contextual, ok := driver.(ContextualDistributedLocker); ok {
		released, err := contextual.ReleaseLockContext(ctx, l.key, l.owner)
		if err == nil {
			l.acquired = false
		}
		return released, err
	}
	released, err := locker.ReleaseLock(l.key, l.owner)
	if err == nil {
		l.acquired = false
	}
	return released, err
}

// Renew 延长已获取锁的租约；驱动不支持续租时返回 ErrCacheLockUnsupported。
func (l *Lock) Renew(ttl time.Duration) (bool, error) {
	return l.RenewContext(context.Background(), ttl)
}

// RenewContext 续租缓存锁并在支持时把调用方上下文传递给驱动。
func (l *Lock) RenewContext(ctx context.Context, ttl time.Duration) (bool, error) {
	if ctx == nil {
		return false, ErrInvalidCacheContext
	}
	if l == nil || l.cache == nil {
		return false, ErrInvalidCacheLock
	}
	if ttl == 0 {
		ttl = defaultLockTTL
	}
	if ttl < 0 || ttl > maxLockTTL {
		return false, fmt.Errorf("%w: %s", ErrInvalidCacheTTL, ttl)
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
	renewer, ok := driver.(LockRenewer)
	if !ok {
		return false, ErrCacheLockUnsupported
	}
	if contextual, ok := driver.(ContextualDistributedLocker); ok {
		renewed, err := contextual.RenewLockContext(ctx, l.key, l.owner, ttl)
		if err == nil && renewed {
			l.ttl = ttl
		}
		if err == nil && !renewed {
			l.acquired = false
		}
		return renewed, err
	}
	renewed, err := renewer.RenewLock(l.key, l.owner, ttl)
	if err == nil && renewed {
		l.ttl = ttl
	}
	if err == nil && !renewed {
		l.acquired = false
	}
	return renewed, err
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

func (c *Cache) resolveTTL(values []time.Duration) (time.Duration, error) {
	if len(values) > 1 {
		return 0, fmt.Errorf("%w: 最多只能指定一个有效期", ErrInvalidCacheTTL)
	}
	if len(values) == 1 {
		if err := validateCacheTTL(values[0]); err != nil {
			return 0, err
		}
		return values[0], nil
	}
	if c == nil || c.state == nil {
		return 0, ErrCacheDriverNotConfigured
	}
	c.state.storesMu.RLock()
	options := c.state.storeOptions[c.storeName]
	c.state.storesMu.RUnlock()
	if err := validateCacheTTL(options.Expire); err != nil {
		return 0, err
	}
	return options.Expire, nil
}

func (c *Cache) trace(action, key string) {
	if c != nil && c.debug != nil {
		c.debug.AddCache(action, key)
	}
}

func (s *cacheState) doRememberContext(ctx context.Context, key string, callback func() (interface{}, error)) (value interface{}, err error) {
	if ctx == nil {
		return nil, ErrInvalidCacheContext
	}
	s.rememberMu.Lock()
	if existing := s.remembering[key]; existing != nil {
		s.rememberMu.Unlock()
		select {
		case <-existing.done:
			return existing.value, existing.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		s.rememberMu.Unlock()
		return nil, err
	}
	call := &rememberCall{done: make(chan struct{})}
	s.remembering[key] = call
	s.rememberMu.Unlock()
	if err := ctx.Err(); err != nil {
		s.finishRemember(key, call, nil, err)
		return nil, err
	}

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

func validatePublicCacheKey(key string) error {
	if key == "" || len(key) > maxCacheKeyBytes || !utf8.ValidString(key) || hasControlCharacter(key) {
		return fmt.Errorf("%w: %q", ErrInvalidCacheKey, key)
	}
	if strings.HasPrefix(key, tagMetaPrefix) || strings.HasPrefix(key, tagReversePrefix) || strings.HasPrefix(key, cacheFencePrefix) || strings.HasPrefix(key, lockKeyPrefix) {
		return fmt.Errorf("%w: %q", ErrReservedCacheKey, key)
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

func sameCacheDriver(first, second Driver) bool {
	firstIdentity, firstOK := cacheDriverIdentity(first)
	secondIdentity, secondOK := cacheDriverIdentity(second)
	return firstOK && secondOK && firstIdentity == secondIdentity
}

func cacheDriverResourceIdentity(driver Driver) (string, bool) {
	identified, ok := driver.(cacheResourceIdentified)
	if !ok {
		return "", false
	}
	identity := strings.TrimSpace(identified.CacheResourceIdentity())
	return identity, identity != ""
}
