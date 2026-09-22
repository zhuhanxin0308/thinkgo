package ratelimit

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/redis/go-redis/v9"
)

const (
	defaultRedisRateLimitPrefix  = "thinkgo:ratelimit:"
	defaultRedisRateLimitTimeout = 3 * time.Second
	maximumRedisRateLimitTimeout = time.Minute
	maximumRedisRateLimitPrefix  = 256
)

// RedisStoreOptions 配置共享限流键空间和单次 Redis 操作上限。
type RedisStoreOptions struct {
	Prefix           string
	OperationTimeout time.Duration
}

// RedisStore 使用 Redis 服务器时间和单键 Lua 脚本执行分布式 GCRA 判定。
// 所有实例只要使用同一 Redis 与前缀，就会共享完全相同的额度状态。
type RedisStore struct {
	client    redis.UniversalClient
	prefix    string
	opTimeout time.Duration
}

var redisGCRAScript = redis.NewScript(`
local current_time = redis.call("TIME")
local now = tonumber(current_time[1]) * 1000000 + tonumber(current_time[2])
local interval = tonumber(ARGV[1])
local tolerance = tonumber(ARGV[2])
local burst = tonumber(ARGV[3])

local stored = redis.call("GET", KEYS[1])
local tat = now
if stored then
    tat = tonumber(stored)
    if not tat then
        return redis.error_reply("invalid stored TAT")
    end
    if tat < now then
        tat = now
    end
end

local allow_at = tat - tolerance
if now < allow_at then
    return {0, 0, allow_at - now, tat - now}
end

local new_tat = tat + interval
local debt = new_tat - now
local remaining = 0
local headroom = tolerance - debt
if headroom >= 0 then
    remaining = math.floor(headroom / interval) + 1
end
if remaining > burst - 1 then
    remaining = burst - 1
end

local ttl_ms = math.floor((debt + 999) / 1000)
if ttl_ms < 1 then
    ttl_ms = 1
end
redis.call("SET", KEYS[1], string.format("%.0f", new_tat), "PX", ttl_ms)
return {1, remaining, 0, debt}
`)

// NewRedisStore 从调用方管理的 Redis 客户端创建共享限流存储。
// 构造器不会接管客户端生命周期，关闭连接仍由创建客户端的一方负责。
func NewRedisStore(client redis.UniversalClient, options RedisStoreOptions) (*RedisStore, error) {
	if isNilRedisRateLimitClient(client) {
		return nil, fmt.Errorf("%w: Redis 客户端不能为空", ErrInvalidConfiguration)
	}
	prefix := options.Prefix
	if prefix == "" {
		prefix = defaultRedisRateLimitPrefix
	}
	if len(prefix) > maximumRedisRateLimitPrefix || containsRedisRateLimitControl(prefix) {
		return nil, fmt.Errorf("%w: Redis 前缀必须不含控制字符且不超过 %d 字节", ErrInvalidConfiguration, maximumRedisRateLimitPrefix)
	}
	opTimeout := options.OperationTimeout
	if opTimeout == 0 {
		opTimeout = defaultRedisRateLimitTimeout
	}
	if opTimeout < 0 || opTimeout > maximumRedisRateLimitTimeout {
		return nil, fmt.Errorf("%w: Redis 操作超时必须在 0 到 %s 之间", ErrInvalidConfiguration, maximumRedisRateLimitTimeout)
	}
	return &RedisStore{client: client, prefix: prefix, opTimeout: opTimeout}, nil
}

// Take 使用 Redis TIME 消除多实例主机时钟偏差；now 参数只供其它 Store 实现使用。
func (store *RedisStore) Take(ctx context.Context, key string, limit Limit, _ time.Time) (Result, error) {
	if store == nil || isNilRedisRateLimitClient(store.client) || store.opTimeout <= 0 || ctx == nil {
		return Result{}, ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	key, err := normalizeKey(key)
	if err != nil {
		return Result{}, err
	}
	interval, err := Validate(limit)
	if err != nil {
		return Result{}, err
	}
	intervalMicros, toleranceMicros, err := redisRateLimitDurations(interval, limit.Burst)
	if err != nil {
		return Result{}, err
	}

	operationContext, cancel := store.operationContext(ctx)
	defer cancel()
	raw, err := redisGCRAScript.Run(
		operationContext,
		store.client,
		[]string{store.prefix + key},
		intervalMicros,
		toleranceMicros,
		limit.Burst,
	).Result()
	if err != nil {
		if contextErr := operationContext.Err(); contextErr != nil {
			return Result{}, contextErr
		}
		return Result{}, fmt.Errorf("redis 限流原子判定失败: %w", err)
	}
	values, ok := raw.([]interface{})
	if !ok || len(values) != 4 {
		return Result{}, fmt.Errorf("%w: 返回值数量或类型错误", ErrInvalidStoreResponse)
	}
	parsed := make([]int64, len(values))
	for index, value := range values {
		parsed[index], err = redisRateLimitInteger(value)
		if err != nil || parsed[index] < 0 {
			return Result{}, fmt.Errorf("%w: 第 %d 项不是非负整数", ErrInvalidStoreResponse, index+1)
		}
	}
	if parsed[0] > 1 || parsed[1] > int64(limit.Burst-1) {
		return Result{}, fmt.Errorf("%w: 判定标志或剩余额度越界", ErrInvalidStoreResponse)
	}
	return Result{
		Allowed:    parsed[0] == 1,
		Limit:      limit.Burst,
		Remaining:  int(parsed[1]),
		RetryAfter: time.Duration(parsed[2]) * time.Microsecond,
		ResetAfter: time.Duration(parsed[3]) * time.Microsecond,
	}, nil
}

// Reset 删除当前命名空间内一个键的额度状态；键不存在时也成功。
func (store *RedisStore) Reset(ctx context.Context, key string) error {
	if store == nil || isNilRedisRateLimitClient(store.client) || store.opTimeout <= 0 || ctx == nil {
		return ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := normalizeKey(key)
	if err != nil {
		return err
	}
	operationContext, cancel := store.operationContext(ctx)
	defer cancel()
	if err := store.client.Del(operationContext, store.prefix+key).Err(); err != nil {
		if contextErr := operationContext.Err(); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("重置 Redis 限流状态失败: %w", err)
	}
	return nil
}

func (store *RedisStore) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if deadline, exists := parent.Deadline(); exists && time.Until(deadline) <= store.opTimeout {
		return parent, func() {}
	}
	return context.WithTimeout(parent, store.opTimeout)
}

func redisRateLimitDurations(interval time.Duration, burst int) (int64, int64, error) {
	if interval < time.Microsecond {
		return 0, 0, fmt.Errorf("%w: Redis 限流最小发射间隔为 %s", ErrInvalidConfiguration, time.Microsecond)
	}
	intervalMicros := int64(interval / time.Microsecond)
	if interval%time.Microsecond != 0 {
		intervalMicros++
	}
	if burst <= 0 || int64(burst-1) > math.MaxInt64/intervalMicros {
		return 0, 0, ErrInvalidConfiguration
	}
	return intervalMicros, int64(burst-1) * intervalMicros, nil
}

func redisRateLimitInteger(value interface{}) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("不支持的 Redis 整数类型 %T", value)
	}
}

func isNilRedisRateLimitClient(client redis.UniversalClient) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func containsRedisRateLimitControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}
