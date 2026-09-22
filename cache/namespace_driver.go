package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// NamespaceDriver 将 ThinkPHP store 的 prefix 与 tag_prefix 应用到所有后端键。
type NamespaceDriver struct {
	driver    Driver
	prefix    string
	tagPrefix string
}

func (driver *NamespaceDriver) supportsTagMutationLock() bool {
	_, supported := driver.driver.(DistributedLocker)
	return supported
}

func (driver *NamespaceDriver) supportsAtomicFencing() bool {
	_, atomic := driver.driver.(AtomicUpdater)
	_, preservesTTL := driver.driver.(TTLAtomicUpdater)
	return atomic && preservesTTL
}

func (driver *NamespaceDriver) supportsGuardedUpdates() bool {
	return cacheDriverSupportsGuardedUpdates(driver.driver)
}

// UpdateIfLockOwnerContext 同时映射数据键与租约键，保留底层的原子提交边界。
func (driver *NamespaceDriver) UpdateIfLockOwnerContext(ctx context.Context, key string, ttl time.Duration, lockKey, owner string, update func(interface{}, bool) (interface{}, bool, error)) (bool, error) {
	if !cacheDriverSupportsGuardedUpdates(driver.driver) {
		return false, ErrCacheLockUnsupported
	}
	return driver.driver.(LockGuardedUpdater).UpdateIfLockOwnerContext(ctx, driver.key(key), ttl, driver.key(lockKey), owner, update)
}

// NewNamespaceDriver 创建缓存命名空间驱动；空业务前缀仍会应用标签前缀规则。
func NewNamespaceDriver(driver Driver, prefix string, tagPrefix string) (*NamespaceDriver, error) {
	if isNilCacheDriver(driver) {
		return nil, ErrCacheDriverNotConfigured
	}
	if tagPrefix == "" {
		tagPrefix = "tag:"
	}
	options := StoreOptions{Prefix: prefix, TagPrefix: tagPrefix}
	probe := NewCache(nil, driver)
	if err := probe.ConfigureStore(defaultStoreName, options); err != nil {
		return nil, err
	}
	return &NamespaceDriver{driver: driver, prefix: prefix, tagPrefix: tagPrefix}, nil
}

func (driver *NamespaceDriver) key(key string) string {
	switch {
	case strings.HasPrefix(key, cacheFencePrefix):
		// fencing 元数据放在业务 namespace 之外，ClearPrefix 清理业务数据时必须保留单调代际。
		digest := sha256.Sum256([]byte(driver.prefix))
		return cacheFencePrefix + "namespace:" + hex.EncodeToString(digest[:]) + ":" + strings.TrimPrefix(key, cacheFencePrefix)
	case strings.HasPrefix(key, tagMetaPrefix):
		return driver.prefix + driver.tagPrefix + strings.TrimPrefix(key, tagMetaPrefix)
	case strings.HasPrefix(key, tagReversePrefix):
		return driver.prefix + driver.tagPrefix + "reverse:" + strings.TrimPrefix(key, tagReversePrefix)
	default:
		return driver.prefix + key
	}
}

func (driver *NamespaceDriver) Get(key string) (interface{}, bool, error) {
	return driver.driver.Get(driver.key(key))
}

func (driver *NamespaceDriver) Set(key string, value interface{}, ttl time.Duration) error {
	return driver.driver.Set(driver.key(key), value, ttl)
}

func (driver *NamespaceDriver) Has(key string) (bool, error) {
	return driver.driver.Has(driver.key(key))
}

func (driver *NamespaceDriver) Delete(key string) error {
	return driver.driver.Delete(driver.key(key))
}

func (driver *NamespaceDriver) Clear() error {
	if driver.prefix == "" {
		return driver.driver.Clear()
	}
	clearer, ok := driver.driver.(PrefixClearer)
	if !ok {
		return ErrCacheNamespaceClearUnsupported
	}
	return clearer.ClearPrefix(driver.prefix)
}

// ValidateClearPrefix 将范围检查传递到实际后端，禁止先发布代际再发现清理能力缺失。
func (driver *NamespaceDriver) ValidateClearPrefix(prefix string) error {
	if _, ok := driver.driver.(ConditionalPrefixClearer); !ok {
		return ErrCacheNamespaceClearUnsupported
	}
	if validator, ok := driver.driver.(PrefixClearValidator); ok {
		return validator.ValidateClearPrefix(driver.prefix + prefix)
	}
	return nil
}

// ClearPrefixIfContext 在物理 namespace 内执行带代际条件的清理，不触及其他 store。
func (driver *NamespaceDriver) ClearPrefixIfContext(ctx context.Context, prefix string, match func(string) bool, remove func(interface{}) (bool, error)) error {
	clearer, ok := driver.driver.(ConditionalPrefixClearer)
	if !ok {
		return ErrCacheNamespaceClearUnsupported
	}
	return clearer.ClearPrefixIfContext(ctx, driver.prefix+prefix, func(physical string) bool {
		logical := strings.TrimPrefix(physical, driver.prefix)
		// Redis 的租约值不是业务 JSON，必须在读取和解码前排除本 namespace 的锁。
		if strings.HasPrefix(logical, lockKeyPrefix) || strings.HasPrefix(logical, cacheFencePrefix) {
			return false
		}
		return match == nil || match(logical)
	}, remove)
}

func (driver *NamespaceDriver) Inc(key string, step int64) (int64, error) {
	return driver.driver.Inc(driver.key(key), step)
}

// IncContext 在命名空间包装后继续传递代际分配期限。
func (driver *NamespaceDriver) IncContext(ctx context.Context, key string, step int64) (int64, error) {
	if ctx == nil {
		return 0, ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if contextual, ok := driver.driver.(ContextualIncrementer); ok {
		return contextual.IncContext(ctx, driver.key(key), step)
	}
	return driver.Inc(key, step)
}

func (driver *NamespaceDriver) Dec(key string, step int64) (int64, error) {
	return driver.driver.Dec(driver.key(key), step)
}

func (driver *NamespaceDriver) GetContext(ctx context.Context, key string) (interface{}, bool, error) {
	if contextual, ok := driver.driver.(ContextualGetter); ok {
		return contextual.GetContext(ctx, driver.key(key))
	}
	return driver.Get(key)
}

func (driver *NamespaceDriver) SetContext(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	if contextual, ok := driver.driver.(ContextualSetter); ok {
		return contextual.SetContext(ctx, driver.key(key), value, ttl)
	}
	return driver.Set(key, value, ttl)
}

func (driver *NamespaceDriver) HasContext(ctx context.Context, key string) (bool, error) {
	if contextual, ok := driver.driver.(ContextualHasser); ok {
		return contextual.HasContext(ctx, driver.key(key))
	}
	return driver.Has(key)
}

func (driver *NamespaceDriver) DeleteContext(ctx context.Context, key string) error {
	if contextual, ok := driver.driver.(ContextualDeleter); ok {
		return contextual.DeleteContext(ctx, driver.key(key))
	}
	return driver.Delete(key)
}

func (driver *NamespaceDriver) ClearContext(ctx context.Context) error {
	if driver.prefix != "" {
		return driver.ClearPrefixContext(ctx, "")
	}
	if contextual, ok := driver.driver.(ContextualClearer); ok {
		return contextual.ClearContext(ctx)
	}
	return driver.Clear()
}

// ClearPrefix 只清理当前命名空间下更窄的逻辑前缀。
func (driver *NamespaceDriver) ClearPrefix(prefix string) error {
	physicalPrefix := driver.prefix + prefix
	if physicalPrefix == "" {
		return ErrInvalidCachePrefix
	}
	clearer, ok := driver.driver.(PrefixClearer)
	if !ok {
		return ErrCacheNamespaceClearUnsupported
	}
	return clearer.ClearPrefix(physicalPrefix)
}

// ClearPrefixContext 在支持时把取消信号传递给底层前缀清理能力。
func (driver *NamespaceDriver) ClearPrefixContext(ctx context.Context, prefix string) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	physicalPrefix := driver.prefix + prefix
	if physicalPrefix == "" {
		return ErrInvalidCachePrefix
	}
	if contextual, ok := driver.driver.(ContextualPrefixClearer); ok {
		return contextual.ClearPrefixContext(ctx, physicalPrefix)
	}
	return driver.ClearPrefix(prefix)
}

// Update 将逻辑键映射后转交底层原子更新能力。
func (driver *NamespaceDriver) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	updater, ok := driver.driver.(AtomicUpdater)
	if !ok {
		return ErrCacheAtomicUpdateUnsupported
	}
	return updater.Update(driver.key(key), ttl, update)
}

// UpdateContext 在支持时把调用方上下文传给底层原子更新能力。
func (driver *NamespaceDriver) UpdateContext(ctx context.Context, key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if updater, ok := driver.driver.(ContextualAtomicUpdater); ok {
		return updater.UpdateContext(ctx, driver.key(key), ttl, update)
	}
	return driver.Update(key, ttl, update)
}

// UpdatePreserveTTL 映射逻辑键后保留底层项的现有过期时间。
func (driver *NamespaceDriver) UpdatePreserveTTL(key string, update func(interface{}, bool) (interface{}, bool, error)) error {
	updater, ok := driver.driver.(TTLAtomicUpdater)
	if !ok {
		return ErrCacheAtomicUpdateUnsupported
	}
	return updater.UpdatePreserveTTL(driver.key(key), update)
}

// UpdatePreserveTTLConditionally 转发同一原子提交的 TTL 决策；旧驱动无法原子重置现存 TTL 时明确拒绝。
func (driver *NamespaceDriver) UpdatePreserveTTLConditionally(key string, update func(interface{}, bool) (interface{}, bool, bool, error)) error {
	if update == nil {
		return ErrNilCacheUpdate
	}
	return atomicUpdateCacheValueConditionalTTL(driver.driver, driver.key(key), update)
}

func (driver *NamespaceDriver) GetMany(keys []string) (map[string]interface{}, error) {
	physical := make([]string, len(keys))
	reverse := make(map[string]string, len(keys))
	for index, key := range keys {
		physical[index] = driver.key(key)
		reverse[physical[index]] = key
	}
	batch, ok := driver.driver.(BatchGetter)
	if !ok {
		result := make(map[string]interface{})
		for _, key := range keys {
			value, found, err := driver.Get(key)
			if err != nil {
				return nil, err
			}
			if found {
				result[key] = value
			}
		}
		return result, nil
	}
	values, err := batch.GetMany(physical)
	return restoreNamespaceKeys(values, reverse), err
}

func (driver *NamespaceDriver) GetManyContext(ctx context.Context, keys []string) (map[string]interface{}, error) {
	physical := make([]string, len(keys))
	reverse := make(map[string]string, len(keys))
	for index, key := range keys {
		physical[index] = driver.key(key)
		reverse[physical[index]] = key
	}
	if batch, ok := driver.driver.(ContextualBatchGetter); ok {
		values, err := batch.GetManyContext(ctx, physical)
		return restoreNamespaceKeys(values, reverse), err
	}
	return driver.GetMany(keys)
}

func (driver *NamespaceDriver) SetMany(values map[string]interface{}, ttl time.Duration) error {
	physical := namespaceValues(driver, values)
	if batch, ok := driver.driver.(BatchSetter); ok {
		return batch.SetMany(physical, ttl)
	}
	for key, value := range values {
		if err := driver.Set(key, value, ttl); err != nil {
			return err
		}
	}
	return nil
}

func (driver *NamespaceDriver) SetManyContext(ctx context.Context, values map[string]interface{}, ttl time.Duration) error {
	if batch, ok := driver.driver.(ContextualBatchSetter); ok {
		return batch.SetManyContext(ctx, namespaceValues(driver, values), ttl)
	}
	return driver.SetMany(values, ttl)
}

func (driver *NamespaceDriver) AcquireLock(key string, owner string, ttl time.Duration) (bool, error) {
	locker, ok := driver.driver.(DistributedLocker)
	if !ok {
		return false, ErrCacheLockUnsupported
	}
	return locker.AcquireLock(driver.key(key), owner, ttl)
}

func (driver *NamespaceDriver) ReleaseLock(key string, owner string) (bool, error) {
	locker, ok := driver.driver.(DistributedLocker)
	if !ok {
		return false, ErrCacheLockUnsupported
	}
	return locker.ReleaseLock(driver.key(key), owner)
}

func (driver *NamespaceDriver) RenewLock(key string, owner string, ttl time.Duration) (bool, error) {
	renewer, ok := driver.driver.(LockRenewer)
	if !ok {
		return false, ErrCacheLockUnsupported
	}
	return renewer.RenewLock(driver.key(key), owner, ttl)
}

func (driver *NamespaceDriver) AcquireLockContext(ctx context.Context, key string, owner string, ttl time.Duration) (bool, error) {
	if locker, ok := driver.driver.(ContextualDistributedLocker); ok {
		return locker.AcquireLockContext(ctx, driver.key(key), owner, ttl)
	}
	return driver.AcquireLock(key, owner, ttl)
}

func (driver *NamespaceDriver) ReleaseLockContext(ctx context.Context, key string, owner string) (bool, error) {
	if locker, ok := driver.driver.(ContextualDistributedLocker); ok {
		return locker.ReleaseLockContext(ctx, driver.key(key), owner)
	}
	return driver.ReleaseLock(key, owner)
}

func (driver *NamespaceDriver) RenewLockContext(ctx context.Context, key string, owner string, ttl time.Duration) (bool, error) {
	if locker, ok := driver.driver.(ContextualDistributedLocker); ok {
		return locker.RenewLockContext(ctx, driver.key(key), owner, ttl)
	}
	return driver.RenewLock(key, owner, ttl)
}

// CacheResourceIdentity 保留底层资源身份，并把逻辑命名空间纳入隔离判断。
func (driver *NamespaceDriver) CacheResourceIdentity() string {
	if identified, ok := driver.driver.(interface{ CacheResourceIdentity() string }); ok {
		identity := strings.TrimSpace(identified.CacheResourceIdentity())
		if identity == "" || driver.prefix == "" {
			return identity
		}
		digest := sha256.Sum256([]byte(driver.prefix))
		return identity + "|namespace:" + hex.EncodeToString(digest[:])
	}
	return ""
}

// Close 关闭底层驱动。
func (driver *NamespaceDriver) Close() error {
	if closer, ok := driver.driver.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func restoreNamespaceKeys(values map[string]interface{}, reverse map[string]string) map[string]interface{} {
	result := make(map[string]interface{}, len(values))
	for key, value := range values {
		if logical, exists := reverse[key]; exists {
			result[logical] = value
		}
	}
	return result
}

func namespaceValues(driver *NamespaceDriver, values map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(values))
	for key, value := range values {
		result[driver.key(key)] = value
	}
	return result
}
