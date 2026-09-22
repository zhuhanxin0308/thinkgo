package driver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/winfile"
)

const fileMutationGuardShards = 64

// Update 通过持有稳定的跨进程文件锁完成单键读改写。
// Session 驱动的所有写入都走该入口，因此租约过期不会让旧请求覆盖较新的墓碑。
func (c *File) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	return c.updateAtomic(key, ttl, false, update)
}

// UpdatePreserveTTL 在跨进程互斥边界内更新值，并保留命中项原有的绝对过期时间。
func (c *File) UpdatePreserveTTL(key string, update func(interface{}, bool) (interface{}, bool, error)) error {
	return c.updateAtomic(key, 0, true, update)
}

// UpdatePreserveTTLConditionally 在跨进程 guard 内一并提交新值与 TTL 决策，不能分成两个写操作。
func (c *File) UpdatePreserveTTLConditionally(key string, update func(interface{}, bool) (interface{}, bool, bool, error)) error {
	return c.updateAtomicTTLPolicyContext(context.Background(), key, 0, update)
}

func (c *File) updateAtomic(key string, ttl time.Duration, preserveTTL bool, update func(interface{}, bool) (interface{}, bool, error)) error {
	return c.updateAtomicContext(context.Background(), key, ttl, preserveTTL, update)
}

// UpdateContext 使文件锁等待继承调用方取消信号，取消不会夺走正在执行回调的所有权。
func (c *File) UpdateContext(ctx context.Context, key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	return c.updateAtomicContext(ctx, key, ttl, false, update)
}

func (c *File) updateAtomicContext(ctx context.Context, key string, ttl time.Duration, preserveTTL bool, update func(interface{}, bool) (interface{}, bool, error)) error {
	var policy func(interface{}, bool) (interface{}, bool, bool, error)
	if update != nil {
		policy = func(value interface{}, found bool) (interface{}, bool, bool, error) {
			next, remove, err := update(value, found)
			return next, remove, preserveTTL, err
		}
	}
	return c.updateAtomicTTLPolicyContext(ctx, key, ttl, policy)
}

func (c *File) updateAtomicTTLPolicyContext(ctx context.Context, key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, bool, error)) error {
	if c == nil || c.path == "" {
		return ErrInvalidCachePath
	}
	if err := validateDriverTTL(ttl); err != nil {
		return err
	}
	if update == nil {
		return ErrNilAtomicUpdate
	}
	if ctx == nil {
		return ErrNilAtomicUpdate
	}
	c.operationMu.RLock()
	defer c.operationMu.RUnlock()
	return c.withMutationGuardContext(ctx, key, func() error {
		lock := c.keyLock(key)
		lock.Lock()
		defer lock.Unlock()
		return c.updateItemLocked(key, ttl, update)
	})
}

func (c *File) updateItemLocked(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, bool, error)) error {
	item, found, err := c.readItemLocked(key, time.Now())
	if err != nil {
		return err
	}
	var current interface{}
	if found {
		if err = json.Unmarshal(item.Value, &current); err != nil {
			return fmt.Errorf("%w: %v", ErrCorruptCacheEntry, err)
		}
	}
	next, remove, preserveTTL, err := update(current, found)
	if err != nil {
		return err
	}
	if remove {
		return c.deleteItemLocked(key)
	}
	expiry := time.Time{}
	if preserveTTL && found {
		expiry = item.Expiry
	}
	if !preserveTTL && ttl > 0 {
		expiry = time.Now().Add(ttl)
	}
	return c.writeItemLocked(key, next, expiry)
}

// ClearPrefix 读取文件项中持久化的原始逻辑键，只删除明确属于指定前缀的条目。
// 缺少 Key 的旧格式条目无法安全归属，因此保留文件并返回可识别错误。
func (c *File) ClearPrefix(prefix string) error {
	if c == nil || c.path == "" {
		return ErrInvalidCachePath
	}
	if prefix == "" {
		return ErrInvalidCachePrefix
	}
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	return c.withAllMutationGuards(context.Background(), func() error { return c.clearPrefixLocked(prefix) })
}

func (c *File) clearPrefixLocked(prefix string) error {
	entries, err := os.ReadDir(c.path)
	if err != nil {
		return err
	}
	var resultErr error
	for _, entry := range entries {
		if entry.IsDir() || !isManagedCacheFilename(entry.Name()) {
			continue
		}
		path := filepath.Join(c.path, entry.Name())
		data, identity, found, readErr := c.readManagedFile(path, maxFileCacheEntryBytes)
		if readErr != nil {
			resultErr = errors.Join(resultErr, readErr)
			continue
		}
		if !found {
			continue
		}
		var item storedItem
		if decodeErr := json.Unmarshal(data, &item); decodeErr != nil || len(item.Value) == 0 {
			resultErr = errors.Join(resultErr, fmt.Errorf("%w: %v", ErrCorruptCacheEntry, decodeErr))
			continue
		}
		if item.Key == "" {
			resultErr = errors.Join(resultErr, fmt.Errorf("%w: %s", ErrUnscopedCacheEntry, entry.Name()))
			continue
		}
		if c.cacheFilePath(item.Key) != path {
			resultErr = errors.Join(resultErr, fmt.Errorf("%w: 文件键摘要不匹配", ErrCorruptCacheEntry))
			continue
		}
		if strings.HasPrefix(item.Key, cacheFenceMetadataPrefix) {
			continue
		}
		if !strings.HasPrefix(item.Key, prefix) {
			continue
		}
		_, removeErr := removeFileIfSame(path, identity)
		resultErr = errors.Join(resultErr, ignoreNotExist(removeErr))
	}
	return resultErr
}

func (c *File) deleteItemLocked(key string) error {
	path := c.cacheFilePath(key)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrUnsafeCacheEntry
	}
	_, err = removeFileIfSame(path, info)
	return err
}

// withMutationGuard 在固定 .guard 文件上持有 OS 级排他锁，锁不依赖过期时间。
func (c *File) withMutationGuard(key string, callback func() error) (resultErr error) {
	return c.withMutationGuardContext(context.Background(), key, callback)
}

func (c *File) withMutationGuardContext(ctx context.Context, key string, callback func() error) error {
	return cacheGuardError(winfile.WithGuard(ctx, c.mutationGuardPath(key), callback))
}

func cacheGuardError(err error) error {
	if errors.Is(err, winfile.ErrUnsafeGuard) {
		return errors.Join(ErrUnsafeCacheEntry, err)
	}
	return err
}

func (c *File) withAllMutationGuards(ctx context.Context, callback func() error) error {
	var acquire func(int) error
	acquire = func(shard int) error {
		if shard == fileMutationGuardShards {
			return callback()
		}
		return winfile.WithGuard(ctx, c.mutationShardGuardPath(shard), func() error { return acquire(shard + 1) })
	}
	return cacheGuardError(acquire(0))
}

// mutationGuardPath 使用固定分片锁约束 guard 文件数量，同时保证同一键跨进程落到同一锁文件。
func (c *File) mutationGuardPath(key string) string {
	digest := sha256.Sum256([]byte(key))
	shard := digest[0] % fileMutationGuardShards
	return c.mutationShardGuardPath(int(shard))
}

func (c *File) mutationShardGuardPath(shard int) string {
	return filepath.Join(c.path, fmt.Sprintf(".mutation-%02x.guard", shard))
}
