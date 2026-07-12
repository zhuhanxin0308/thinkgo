package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	defaultRedisHost      = "127.0.0.1"
	defaultRedisPort      = 6379
	defaultRedisOpTimeout = 3 * time.Second
	maxRedisDatabase      = 1024
	maxRedisTimeoutMS     = 60_000
	maxRedisPrefixBytes   = 256
	managerLockKeyPrefix  = "__thinkgo_lock__:"
)

var redisHostnameLabelPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// Redis 基于 go-redis 实现缓存、原子计数和分布式锁，并显式传播后端错误。
type Redis struct {
	client       *redis.Client
	prefix       string
	opTimeout    time.Duration
	allowFlushDB bool
	closeOnce    sync.Once
	closeErr     error
}

// NewRedis 严格校验 Redis 配置，避免类型截断、无界超时和错误地址延迟到运行期。
func NewRedis(config map[string]interface{}) (*Redis, error) {
	allowed := map[string]struct{}{
		"host": {}, "port": {}, "password": {}, "select": {},
		"timeout_ms": {}, "prefix": {}, "allow_flush_db": {},
	}
	for key := range config {
		if _, ok := allowed[key]; !ok {
			return nil, invalidRedisConfig(key, "未知配置项")
		}
	}

	host := defaultRedisHost
	if raw, ok := config["host"]; ok {
		value, valid := raw.(string)
		if !valid || !validRedisHost(value) {
			return nil, invalidRedisConfig("host", "必须是有效的 IP 地址或主机名")
		}
		host = value
	}

	port := int64(defaultRedisPort)
	if raw, ok := config["port"]; ok {
		value, err := redisConfigInteger(raw)
		if err != nil || value < 1 || value > 65535 {
			return nil, invalidRedisConfig("port", "必须是 1 至 65535 的整数")
		}
		port = value
	}

	password := ""
	if raw, ok := config["password"]; ok {
		value, valid := raw.(string)
		if !valid {
			return nil, invalidRedisConfig("password", "必须是字符串")
		}
		password = value
	}

	database := int64(0)
	if raw, ok := config["select"]; ok {
		value, err := redisConfigInteger(raw)
		if err != nil || value < 0 || value > maxRedisDatabase {
			return nil, invalidRedisConfig("select", "必须是受支持的非负整数")
		}
		database = value
	}

	opTimeout := defaultRedisOpTimeout
	if raw, ok := config["timeout_ms"]; ok {
		value, err := redisConfigInteger(raw)
		if err != nil || value < 0 || value > maxRedisTimeoutMS {
			return nil, invalidRedisConfig("timeout_ms", "必须是 0 至 60000 的整数")
		}
		if value > 0 {
			opTimeout = time.Duration(value) * time.Millisecond
		}
	}

	prefix := ""
	if raw, ok := config["prefix"]; ok {
		value, valid := raw.(string)
		if !valid || len(value) > maxRedisPrefixBytes || containsControlCharacter(value) {
			return nil, invalidRedisConfig("prefix", "必须是不含控制字符且不超过 256 字节的字符串")
		}
		prefix = value
	}

	allowFlushDB := false
	if raw, ok := config["allow_flush_db"]; ok {
		value, valid := raw.(bool)
		if !valid {
			return nil, invalidRedisConfig("allow_flush_db", "必须是布尔值")
		}
		allowFlushDB = value
	}

	client := redis.NewClient(&redis.Options{
		Addr:         net.JoinHostPort(host, strconv.FormatInt(port, 10)),
		Password:     password,
		DB:           int(database),
		DialTimeout:  opTimeout,
		ReadTimeout:  opTimeout,
		WriteTimeout: opTimeout,
	})
	return &Redis{
		client:       client,
		prefix:       prefix,
		opTimeout:    opTimeout,
		allowFlushDB: allowFlushDB,
	}, nil
}

func invalidRedisConfig(field, reason string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidRedisConfig, field, reason)
}

func validRedisHost(host string) bool {
	if host == "" || len(host) > 253 || containsControlCharacter(host) || strings.ContainsAny(host, " \t\r\n") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	trimmed := strings.TrimSuffix(host, ".")
	if trimmed == "" {
		return false
	}
	for _, label := range strings.Split(trimmed, ".") {
		if !redisHostnameLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func redisConfigInteger(value interface{}) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, ErrInvalidRedisConfig
		}
		return int64(typed), nil
	case uint8:
		return int64(typed), nil
	case uint16:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		if typed > math.MaxInt64 {
			return 0, ErrInvalidRedisConfig
		}
		return int64(typed), nil
	case float32:
		return exactRedisFloat(float64(typed))
	case float64:
		return exactRedisFloat(typed)
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 64)
		if err != nil {
			return 0, ErrInvalidRedisConfig
		}
		return parsed, nil
	default:
		return 0, ErrInvalidRedisConfig
	}
}

func exactRedisFloat(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
		value < float64(math.MinInt64) || value >= float64(math.MaxInt64) {
		return 0, ErrInvalidRedisConfig
	}
	return int64(value), nil
}

func (c *Redis) ctxWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), c.opTimeout)
}

func (c *Redis) withPrefix(key string) string {
	return c.prefix + key
}

func (c *Redis) validateClient() error {
	if c == nil || c.client == nil || c.opTimeout <= 0 {
		return ErrInvalidRedisClient
	}
	return nil
}

func (c *Redis) Get(key string) (interface{}, bool, error) {
	if err := c.validateClient(); err != nil {
		return nil, false, err
	}
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()
	raw, err := c.client.Get(ctx, c.withPrefix(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var value interface{}
	if err = json.Unmarshal(raw, &value); err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrCorruptCacheEntry, err)
	}
	return value, true, nil
}

func (c *Redis) Set(key string, value interface{}, ttl time.Duration) error {
	if err := c.validateClient(); err != nil {
		return err
	}
	if err := validateDriverTTL(ttl); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()
	return c.client.Set(ctx, c.withPrefix(key), encoded, ttl).Err()
}

func (c *Redis) Has(key string) (bool, error) {
	if err := c.validateClient(); err != nil {
		return false, err
	}
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()
	count, err := c.client.Exists(ctx, c.withPrefix(key)).Result()
	return count > 0, err
}

func (c *Redis) Delete(key string) error {
	if err := c.validateClient(); err != nil {
		return err
	}
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()
	return c.client.Del(ctx, c.withPrefix(key)).Err()
}

// Clear 仅清除当前前缀；空前缀必须显式授权才允许执行 FLUSHDB。
func (c *Redis) Clear() error {
	if err := c.validateClient(); err != nil {
		return err
	}
	if c.prefix == "" && !c.allowFlushDB {
		return ErrUnsafeRedisFlush
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.opTimeout*5)
	defer cancel()
	if c.prefix == "" {
		return c.client.FlushDB(ctx).Err()
	}

	var cursor uint64
	pattern := redisScanPattern(c.prefix)
	for {
		keys, next, err := c.client.Scan(ctx, cursor, pattern, 256).Result()
		if err != nil {
			return err
		}
		// 锁与普通缓存共享 Redis DB，但 Flush 不能破坏仍在执行的临界区。
		deletable := keys[:0]
		lockPrefix := c.withPrefix(managerLockKeyPrefix)
		for _, key := range keys {
			if !strings.HasPrefix(key, lockPrefix) {
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

// redisScanPattern 转义 Redis glob 元字符，确保前缀只按字面量匹配。
func redisScanPattern(prefix string) string {
	var pattern strings.Builder
	pattern.Grow(len(prefix) + 1)
	for _, character := range prefix {
		switch character {
		case '*', '?', '[', ']', '\\':
			pattern.WriteByte('\\')
		}
		pattern.WriteRune(character)
	}
	pattern.WriteByte('*')
	return pattern.String()
}

func (c *Redis) Inc(key string, step int64) (int64, error) {
	if err := c.validateClient(); err != nil {
		return 0, err
	}
	if err := validateCounterStep(step); err != nil {
		return 0, err
	}
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()
	return c.client.IncrBy(ctx, c.withPrefix(key), step).Result()
}

func (c *Redis) Dec(key string, step int64) (int64, error) {
	if err := c.validateClient(); err != nil {
		return 0, err
	}
	if err := validateCounterStep(step); err != nil {
		return 0, err
	}
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()
	return c.client.DecrBy(ctx, c.withPrefix(key), step).Result()
}

// AcquireLock 使用 SET NX PX 原子获取分布式锁。
func (c *Redis) AcquireLock(key string, owner string, ttl time.Duration) (bool, error) {
	if err := c.validateClient(); err != nil {
		return false, err
	}
	if owner == "" || ttl <= 0 {
		return false, ErrInvalidCacheLock
	}
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()
	return c.client.SetNX(ctx, c.withPrefix(key), owner, ttl).Result()
}

// ReleaseLock 通过 Lua 脚本保证只有 owner 匹配时才删除锁。
func (c *Redis) ReleaseLock(key string, owner string) (bool, error) {
	if err := c.validateClient(); err != nil {
		return false, err
	}
	if owner == "" {
		return false, ErrInvalidCacheLock
	}
	const releaseScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`
	ctx, cancel := c.ctxWithTimeout()
	defer cancel()
	result, err := c.client.Eval(ctx, releaseScript, []string{c.withPrefix(key)}, owner).Int64()
	return result > 0, err
}

// Close 幂等释放 Redis 连接池。
func (c *Redis) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.closeErr = c.client.Close()
	})
	return c.closeErr
}
