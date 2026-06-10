package driver

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis 基于 Redis 实现缓存驱动，支持原子计数和分布式锁。
type Redis struct {
	client *redis.Client
	ctx    context.Context
}

// NewRedis 创建 Redis 缓存驱动。
func NewRedis(config map[string]interface{}) *Redis {
	addr := "127.0.0.1:6379"
	if value, ok := config["host"].(string); ok {
		addr = value
		if port, ok := config["port"].(float64); ok {
			addr = fmt.Sprintf("%s:%d", addr, int(port))
		} else if port, ok := config["port"].(int); ok {
			addr = fmt.Sprintf("%s:%d", addr, port)
		}
	}

	password := ""
	if value, ok := config["password"].(string); ok {
		password = value
	}

	dbIndex := 0
	if value, ok := config["select"].(float64); ok {
		dbIndex = int(value)
	} else if value, ok := config["select"].(int); ok {
		dbIndex = value
	}

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       dbIndex,
	})
	return &Redis{
		client: client,
		ctx:    context.Background(),
	}
}

func (c *Redis) Get(key string) interface{} {
	val, err := c.client.Get(c.ctx, key).Result()
	if err == redis.Nil || err != nil {
		return nil
	}
	return val
}

func (c *Redis) Set(key string, val interface{}, ttl time.Duration) {
	c.client.Set(c.ctx, key, val, ttl)
}

func (c *Redis) Has(key string) bool {
	return c.Get(key) != nil
}

func (c *Redis) Delete(key string) {
	c.client.Del(c.ctx, key)
}

func (c *Redis) Clear() {
	c.client.FlushDB(c.ctx)
}

func (c *Redis) Inc(key string, step int64) int64 {
	val, _ := c.client.IncrBy(c.ctx, key, step).Result()
	return val
}

func (c *Redis) Dec(key string, step int64) int64 {
	val, _ := c.client.DecrBy(c.ctx, key, step).Result()
	return val
}

// AcquireLock 使用 SET NX EX 获取分布式锁。
func (c *Redis) AcquireLock(key string, owner string, ttl time.Duration) bool {
	acquired, _ := c.client.SetNX(c.ctx, key, owner, ttl).Result()
	return acquired
}

// ReleaseLock 通过 Lua 脚本保证只有 owner 匹配时才能删除锁。
func (c *Redis) ReleaseLock(key string, owner string) bool {
	const releaseScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`
	result, err := c.client.Eval(c.ctx, releaseScript, []string{key}, owner).Int64()
	return err == nil && result > 0
}
