package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisExpiryMissing   = -2
	redisExpiryPermanent = -1
)

// UpdateWithExpiryContext 同时 WATCH 值及过期时间，恢复时直接提交服务端原始绝对过期时刻。
func (c *Redis) UpdateWithExpiryContext(parent context.Context, key string, ttl time.Duration, update func(interface{}, bool, time.Time) (interface{}, bool, *time.Time, error)) error {
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
	physical := c.withPrefix(key)
	for attempt := 0; attempt < maxRedisAtomicUpdateRetries; attempt++ {
		err := c.client.Watch(ctx, func(transaction *redis.Tx) error {
			raw, err := transaction.Get(ctx, physical).Bytes()
			found := !errors.Is(err, redis.Nil)
			if err != nil && found {
				return err
			}
			var expiry time.Time
			var current interface{}
			if found {
				milliseconds, err := transaction.Do(ctx, "PEXPIRETIME", physical).Int64()
				if err != nil {
					return err
				}
				switch milliseconds {
				case redisExpiryMissing:
					found = false
				case redisExpiryPermanent:
				default:
					expiry = time.UnixMilli(milliseconds)
				}
				if found {
					current, err = decodeRedisJSON(raw)
					if err != nil {
						return err
					}
				}
			}
			next, remove, override, err := update(current, found, expiry)
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
			_, err = transaction.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				switch {
				case remove:
					pipe.Del(ctx, physical)
				case override != nil && !override.IsZero():
					pipe.SetArgs(ctx, physical, encoded, redis.SetArgs{ExpireAt: *override})
				case override != nil:
					pipe.Set(ctx, physical, encoded, 0)
				default:
					pipe.Set(ctx, physical, encoded, ttl)
				}
				return nil
			})
			return err
		}, physical)
		if !errors.Is(err, redis.TxFailedErr) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: Redis 过期时间快照重试超过 %d 次", ErrAtomicUpdateConflict, maxRedisAtomicUpdateRetries)
}
