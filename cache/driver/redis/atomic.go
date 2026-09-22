package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const maxRedisAtomicUpdateRetries = 64

// Update 使用兼容上下文执行 Redis 单键乐观事务。
func (c *Redis) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	return c.updateAtomicContext(context.Background(), key, ttl, false, update)
}

// UpdateContext 通过 WATCH/MULTI 保证只有基于当前版本计算的结果能够提交。
func (c *Redis) UpdateContext(parent context.Context, key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	return c.updateAtomicContext(parent, key, ttl, false, update)
}

// UpdatePreserveTTL 原子更新值，并通过 Redis KEEPTTL 保留命中项当前的剩余有效期。
func (c *Redis) UpdatePreserveTTL(key string, update func(interface{}, bool) (interface{}, bool, error)) error {
	return c.updateAtomicContext(context.Background(), key, 0, true, update)
}

// UpdatePreserveTTLConditionally 在每次 WATCH 尝试中重新决定 TTL，并与最终值一起事务提交。
func (c *Redis) UpdatePreserveTTLConditionally(key string, update func(interface{}, bool) (interface{}, bool, bool, error)) error {
	return c.updateAtomicTTLPolicyContext(context.Background(), key, 0, update)
}

func (c *Redis) updateAtomicContext(parent context.Context, key string, ttl time.Duration, preserveTTL bool, update func(interface{}, bool) (interface{}, bool, error)) error {
	var policy func(interface{}, bool) (interface{}, bool, bool, error)
	if update != nil {
		policy = func(value interface{}, found bool) (interface{}, bool, bool, error) {
			next, remove, err := update(value, found)
			return next, remove, preserveTTL, err
		}
	}
	return c.updateAtomicTTLPolicyContext(parent, key, ttl, policy)
}

func (c *Redis) updateAtomicTTLPolicyContext(parent context.Context, key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, bool, error)) error {
	if parent == nil {
		return ErrInvalidRedisContext
	}
	if err := c.validateClient(); err != nil {
		return err
	}
	if err := validateDriverTTL(ttl); err != nil {
		return err
	}
	if update == nil {
		return ErrNilAtomicUpdate
	}
	ctx, cancel := c.contextWithParent(parent)
	defer cancel()
	physicalKey := c.withPrefix(key)
	for attempt := 0; attempt < maxRedisAtomicUpdateRetries; attempt++ {
		err := c.client.Watch(ctx, func(transaction *redis.Tx) error {
			raw, getErr := transaction.Get(ctx, physicalKey).Bytes()
			found := true
			if errors.Is(getErr, redis.Nil) {
				found = false
				getErr = nil
			}
			if getErr != nil {
				return getErr
			}
			var current interface{}
			if found {
				current, getErr = decodeRedisJSON(raw)
				if getErr != nil {
					return getErr
				}
			}
			next, remove, preserveTTL, updateErr := update(current, found)
			if updateErr != nil {
				return updateErr
			}
			var encoded []byte
			if !remove {
				encoded, updateErr = json.Marshal(next)
				if updateErr != nil {
					return updateErr
				}
			}
			_, updateErr = transaction.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				if remove {
					pipe.Del(ctx, physicalKey)
				} else if preserveTTL {
					pipe.SetArgs(ctx, physicalKey, encoded, redis.SetArgs{KeepTTL: true})
				} else {
					pipe.Set(ctx, physicalKey, encoded, ttl)
				}
				return nil
			})
			return updateErr
		}, physicalKey)
		if !errors.Is(err, redis.TxFailedErr) {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: Redis 重试超过 %d 次", ErrAtomicUpdateConflict, maxRedisAtomicUpdateRetries)
}

// ClearPrefix 只清理当前 Redis namespace 下的指定逻辑前缀。
func (c *Redis) ClearPrefix(prefix string) error {
	return c.ClearPrefixContext(context.Background(), prefix)
}

// ClearPrefixContext 使用字面量 SCAN 前缀并保留活动锁。
func (c *Redis) ClearPrefixContext(parent context.Context, prefix string) error {
	if parent == nil {
		return ErrInvalidRedisContext
	}
	if err := c.validateClient(); err != nil {
		return err
	}
	if prefix == "" {
		return ErrInvalidCachePrefix
	}
	ctx, cancel := context.WithTimeout(parent, c.opTimeout*5)
	defer cancel()
	physicalPrefix := c.withPrefix(prefix)
	pattern := redisScanPattern(physicalPrefix)
	lockPrefix := c.withPrefix(managerLockKeyPrefix)
	fencePrefix := c.withPrefix(cacheFenceMetadataPrefix)
	var cursor uint64
	for {
		keys, next, err := c.client.Scan(ctx, cursor, pattern, 256).Result()
		if err != nil {
			return err
		}
		deletable := keys[:0]
		for _, key := range keys {
			if strings.HasPrefix(key, physicalPrefix) && !strings.HasPrefix(key, lockPrefix) && !strings.HasPrefix(key, fencePrefix) {
				deletable = append(deletable, key)
			}
		}
		if len(deletable) > 0 {
			if err = c.client.Del(ctx, deletable...).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}
