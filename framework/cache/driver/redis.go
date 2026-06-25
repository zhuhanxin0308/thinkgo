package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// defaultRedisOpTimeout 单次 Redis 操作的默认超时，避免后端抖动时请求 goroutine 无限阻塞。
const defaultRedisOpTimeout = 3 * time.Second

// Redis 基于 Redis 实现缓存驱动，支持原子计数和分布式锁。
//
// 所有值统一以 JSON 序列化后写入、读取时反序列化，保证与 File 驱动行为一致
// （可缓存 map/struct/slice，而不仅是字符串/数值）。
type Redis struct {
	client    *redis.Client
	prefix    string
	opTimeout time.Duration
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

	prefix := ""
	if value, ok := config["prefix"].(string); ok {
		prefix = value
	}

	opTimeout := defaultRedisOpTimeout
	if value, ok := config["timeout_ms"].(float64); ok && value > 0 {
		opTimeout = time.Duration(value) * time.Millisecond
	} else if value, ok := config["timeout_ms"].(int); ok && value > 0 {
		opTimeout = time.Duration(value) * time.Millisecond
	}

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       dbIndex,
	})
	return &Redis{
		client:    client,
		prefix:    prefix,
		opTimeout: opTimeout,
	}
}

// ctx 返回带超时的操作上下文。
func (c *Redis) ctxWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), c.opTimeout)
}

// withPrefix 为缓存键统一追加前缀，便于 Clear 按前缀安全清理。
func (c *Redis) withPrefix(key string) string {
	return c.prefix + key
}

func (c *Redis) Get(key string) interface{} {
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()

	raw, err := c.client.Get(ctx, c.withPrefix(key)).Bytes()
	if err != nil {
		// redis.Nil（键不存在）与其它错误统一按缓存未命中处理。
		return nil
	}

	// 优先按 JSON 反序列化（Set 写入的格式）；非法 JSON（如 INCRBY 产生的裸整数或外部写入的原始串）回退为原始字符串。
	var value interface{}
	if jsonErr := json.Unmarshal(raw, &value); jsonErr == nil {
		return value
	}
	return string(raw)
}

func (c *Redis) Set(key string, val interface{}, ttl time.Duration) {
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()

	encoded, err := json.Marshal(val)
	if err != nil {
		// 无法序列化的值不写入，避免静默存入非法数据后读取异常。
		return
	}
	c.client.Set(ctx, c.withPrefix(key), encoded, ttl)
}

func (c *Redis) Has(key string) bool {
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()

	count, err := c.client.Exists(ctx, c.withPrefix(key)).Result()
	return err == nil && count > 0
}

func (c *Redis) Delete(key string) {
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()
	c.client.Del(ctx, c.withPrefix(key))
}

// Clear 清空当前驱动管理的缓存。
// 配置了 prefix 时仅按前缀扫描删除，避免误清同库内其它业务数据；
// 未配置 prefix 时回退为 FlushDB（仅影响所选 DB 索引），建议生产环境务必配置独立 prefix 或 DB。
func (c *Redis) Clear() {
	ctx, cancel := context.WithTimeout(context.Background(), c.opTimeout*5)
	defer cancel()

	if c.prefix == "" {
		c.client.FlushDB(ctx)
		return
	}

	var cursor uint64
	pattern := c.prefix + "*"
	for {
		keys, next, err := c.client.Scan(ctx, cursor, pattern, 256).Result()
		if err != nil {
			return
		}
		if len(keys) > 0 {
			c.client.Del(ctx, keys...)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
}

func (c *Redis) Inc(key string, step int64) int64 {
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()
	val, _ := c.client.IncrBy(ctx, c.withPrefix(key), step).Result()
	return val
}

func (c *Redis) Dec(key string, step int64) int64 {
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()
	val, _ := c.client.DecrBy(ctx, c.withPrefix(key), step).Result()
	return val
}

// AcquireLock 使用 SET NX EX 获取分布式锁。
func (c *Redis) AcquireLock(key string, owner string, ttl time.Duration) bool {
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()
	acquired, _ := c.client.SetNX(ctx, c.withPrefix(key), owner, ttl).Result()
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
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()
	result, err := c.client.Eval(ctx, releaseScript, []string{c.withPrefix(key)}, owner).Int64()
	return err == nil && result > 0
}
