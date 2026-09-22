package driver

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/framework/internal/winfile"
)

var errConditionalClearPreserved = errors.New("条件清理保留当前缓存值")

// ClearPrefixIfContext 先快照候选键，再在每个键的原子边界内核验当前值，保护并发新写入。
func (c *Memory) ClearPrefixIfContext(ctx context.Context, prefix string, match func(string) bool, remove func(interface{}) (bool, error)) error {
	if c == nil || ctx == nil || remove == nil {
		return ErrNilAtomicUpdate
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.lock.RLock()
	keys := make([]string, 0, len(c.items))
	for key := range c.items {
		if strings.HasPrefix(key, prefix) && !isCapacityExemptKey(key) && (match == nil || match(key)) {
			keys = append(keys, key)
		}
	}
	c.lock.RUnlock()
	return clearKeysConditionally(ctx, keys, c.UpdatePreserveTTL, remove)
}

// ClearPrefixIfContext 利用持久化原始键查找范围，删除前重新持有跨进程 mutation guard。
func (c *File) ClearPrefixIfContext(ctx context.Context, prefix string, match func(string) bool, remove func(interface{}) (bool, error)) error {
	if c == nil || c.path == "" {
		return ErrInvalidCachePath
	}
	if ctx == nil || remove == nil {
		return ErrNilAtomicUpdate
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(c.path)
	if err != nil {
		return err
	}
	var result error
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if !isManagedCacheFilename(entry.Name()) || entry.IsDir() {
			continue
		}
		path := filepath.Join(c.path, entry.Name())
		digest, _ := hex.DecodeString(strings.TrimSuffix(entry.Name(), ".cache"))
		var data []byte
		var found bool
		readErr := winfile.WithGuard(ctx, c.mutationShardGuardPath(int(digest[0])%fileMutationGuardShards), func() error {
			var err error
			data, _, found, err = c.readManagedFile(path, maxFileCacheEntryBytes)
			return err
		})
		if readErr != nil {
			result = errors.Join(result, readErr)
			continue
		}
		if !found {
			continue
		}
		var item storedItem
		if err := json.Unmarshal(data, &item); err != nil || len(item.Value) == 0 {
			result = errors.Join(result, ErrCorruptCacheEntry, err)
			continue
		}
		if item.Key == "" || c.cacheFilePath(item.Key) != path {
			result = errors.Join(result, ErrUnscopedCacheEntry)
			continue
		}
		if strings.HasPrefix(item.Key, prefix) && !strings.HasPrefix(item.Key, cacheFenceMetadataPrefix) && (match == nil || match(item.Key)) {
			keys = append(keys, item.Key)
		}
	}
	return errors.Join(result, clearKeysConditionally(ctx, keys, func(key string, update func(interface{}, bool) (interface{}, bool, error)) error {
		return c.updateAtomicContext(ctx, key, 0, true, update)
	}, remove))
}

func clearKeysConditionally(ctx context.Context, keys []string, update func(string, func(interface{}, bool) (interface{}, bool, error)) error, remove func(interface{}) (bool, error)) error {
	var result error
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		err := update(key, func(value interface{}, found bool) (interface{}, bool, error) {
			if !found {
				return nil, false, errConditionalClearPreserved
			}
			deleted, err := remove(value)
			if err != nil {
				return nil, false, err
			}
			if !deleted {
				return nil, false, errConditionalClearPreserved
			}
			return nil, true, nil
		})
		if !errors.Is(err, errConditionalClearPreserved) {
			result = errors.Join(result, err)
		}
	}
	return result
}
