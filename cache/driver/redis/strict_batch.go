package redis

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// GetManyStrictContext 按顺序通过一个 pipeline 读取，保留单个 GET 的键类型错误。
func (c *Redis) GetManyStrictContext(parent context.Context, keys []string) (map[string]interface{}, error) {
	if parent == nil {
		return nil, ErrInvalidRedisContext
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if err := c.validateClient(); err != nil {
		return nil, err
	}
	if len(keys) > maxRedisBatchEntries {
		return nil, fmt.Errorf("%w: Redis GET pipeline 键数量超过 %d", ErrCacheBatchTooLarge, maxRedisBatchEntries)
	}
	values := make(map[string]interface{}, len(keys))
	if len(keys) == 0 {
		return values, nil
	}
	ctx, cancel := c.contextWithParent(parent)
	defer cancel()
	commands := make([]*redis.StringCmd, len(keys))
	_, err := c.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for index, key := range keys {
			commands[index] = pipe.Get(ctx, c.withPrefix(key))
		}
		return nil
	})
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	for index, command := range commands {
		// Pipeline 的首个错误可能是缺键；每条命令仍需检查类型和网络错误。
		raw, err := command.Bytes()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		value, err := decodeRedisJSON(raw)
		if err != nil {
			return nil, err
		}
		values[keys[index]] = value
	}
	return values, nil
}
