package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var errRedisLeaseChanged = errors.New("Redis 缓存提交租约已变更")

// 提交脚本在真正写入时再次检查 owner，不依赖 Redis 对过期 WATCH 键的版本差异。
const redisLeaseCommitScript = `
if redis.call("GET", KEYS[2]) ~= ARGV[1] then
	return 0
end
if ARGV[2] == "1" then
	redis.call("DEL", KEYS[1])
elseif tonumber(ARGV[4]) > 0 then
	redis.call("SET", KEYS[1], ARGV[3], "PX", ARGV[4])
else
	redis.call("SET", KEYS[1], ARGV[3])
end
return 1
`

// UpdateIfLockOwnerContext 用 WATCH 保护数据版本，用原子脚本保护提交时的租约。
func (c *Redis) UpdateIfLockOwnerContext(parent context.Context, key string, ttl time.Duration, lockKey, owner string, update func(interface{}, bool) (interface{}, bool, error)) (bool, error) {
	if parent == nil {
		return false, ErrInvalidRedisContext
	}
	if err := c.validateClient(); err != nil {
		return false, err
	}
	if lockKey == "" || owner == "" || lockKey == key {
		return false, ErrInvalidCacheLock
	}
	if err := validateDriverTTL(ttl); err != nil {
		return false, err
	}
	if update == nil {
		return false, ErrNilAtomicUpdate
	}
	ctx, cancel := c.contextWithParent(parent)
	defer cancel()
	physicalKey, physicalLock := c.withPrefix(key), c.withPrefix(lockKey)
	milliseconds := ttl.Milliseconds()
	if ttl > 0 && milliseconds == 0 {
		milliseconds = 1
	}
	for attempt := 0; attempt < maxRedisAtomicUpdateRetries; attempt++ {
		err := c.client.Watch(ctx, func(transaction *redis.Tx) error {
			leaseOwner, err := transaction.Get(ctx, physicalLock).Result()
			if errors.Is(err, redis.Nil) {
				return errRedisLeaseChanged
			}
			if err != nil {
				return err
			}
			if leaseOwner != owner {
				return errRedisLeaseChanged
			}
			raw, err := transaction.Get(ctx, physicalKey).Bytes()
			found := !errors.Is(err, redis.Nil)
			if err != nil && found {
				return err
			}
			var value interface{}
			if found {
				value, err = decodeRedisJSON(raw)
				if err != nil {
					return err
				}
			}
			next, remove, err := update(value, found)
			if err != nil {
				return err
			}
			var encoded []byte
			if !remove {
				encoded, err = json.Marshal(next)
				if err != nil {
					return err
				}
			}
			var result *redis.Cmd
			_, err = transaction.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				result = pipe.Eval(ctx, redisLeaseCommitScript, []string{physicalKey, physicalLock}, owner, remove, encoded, milliseconds)
				return nil
			})
			if err != nil {
				return err
			}
			committed, err := result.Int64()
			if err == nil && committed == 0 {
				return errRedisLeaseChanged
			}
			return err
		}, physicalKey)
		if errors.Is(err, errRedisLeaseChanged) {
			return false, nil
		}
		if !errors.Is(err, redis.TxFailedErr) {
			return err == nil, err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
	}
	return false, fmt.Errorf("%w: Redis 租约提交重试超过 %d 次", ErrAtomicUpdateConflict, maxRedisAtomicUpdateRetries)
}
