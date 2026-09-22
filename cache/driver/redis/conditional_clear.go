package redis

import (
	"context"
	"errors"
	"strings"
)

var errConditionalClearPreserved = errors.New("条件清理保留当前 Redis 值")

// ValidateClearPrefix 在应用层发布 fencing 之前拒绝未经授权的无前缀全库失效。
func (c *Redis) ValidateClearPrefix(prefix string) error {
	if err := c.validateClient(); err != nil {
		return err
	}
	if prefix == "" && c.prefix == "" && !c.allowFlushDB {
		return ErrUnsafeRedisFlush
	}
	return nil
}

// ClearPrefixIfContext 使用 WATCH 保护每次条件删除，SCAN 后发生的新写入必须重新检查。
func (c *Redis) ClearPrefixIfContext(parent context.Context, prefix string, match func(string) bool, remove func(interface{}) (bool, error)) error {
	if parent == nil {
		return ErrInvalidRedisContext
	}
	if err := c.ValidateClearPrefix(prefix); err != nil {
		return err
	}
	if remove == nil {
		return ErrNilAtomicUpdate
	}
	ctx, cancel := context.WithTimeout(parent, c.opTimeout*5)
	defer cancel()
	physicalPrefix := c.withPrefix(prefix)
	lockPrefix := c.withPrefix(managerLockKeyPrefix)
	fencePrefix := c.withPrefix(cacheFenceMetadataPrefix)
	var cursor uint64
	for {
		keys, next, err := c.client.Scan(ctx, cursor, redisScanPattern(physicalPrefix), 256).Result()
		if err != nil {
			return err
		}
		for _, key := range keys {
			if !strings.HasPrefix(key, physicalPrefix) || strings.HasPrefix(key, lockPrefix) || strings.HasPrefix(key, fencePrefix) {
				continue
			}
			if match != nil && !match(strings.TrimPrefix(key, c.prefix)) {
				continue
			}
			err = c.updateAtomicContext(ctx, strings.TrimPrefix(key, c.prefix), 0, true, func(value interface{}, found bool) (interface{}, bool, error) {
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
			if err != nil && !errors.Is(err, errConditionalClearPreserved) {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}
