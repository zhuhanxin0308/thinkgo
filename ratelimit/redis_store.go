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
local now_sec = tonumber(current_time[1])
local now_usec = tonumber(current_time[2])
local int_sec = tonumber(ARGV[1])
local int_usec = tonumber(ARGV[2])
local tol_sec = tonumber(ARGV[3])
local tol_usec = tonumber(ARGV[4])
local burst = tonumber(ARGV[5])

local function lt(s1, u1, s2, u2)
    return s1 < s2 or (s1 == s2 and u1 < u2)
end

local function add(s1, u1, s2, u2)
    local s = s1 + s2
    local u = u1 + u2
    if u >= 1000000 then
        s = s + 1
        u = u - 1000000
    end
    return s, u
end

local function sub(s1, u1, s2, u2)
    local s = s1 - s2
    local u = u1 - u2
    if u < 0 then
        s = s - 1
        u = u + 1000000
    end
    return s, u
end

local function mul(k, s, u)
    local pu = k * u
    local ps = k * s + math.floor(pu / 1000000)
    pu = pu % 1000000
    return ps, pu
end

local stored = redis.call("GET", KEYS[1])
local tat_sec = now_sec
local tat_usec = now_usec
if stored then
    local s_str, u_str = string.match(stored, "^(%d+):(%d+)$")
    if s_str and u_str then
        tat_sec = tonumber(s_str)
        tat_usec = tonumber(u_str)
        if not tat_sec or not tat_usec or tat_usec >= 1000000 then
            return redis.error_reply("invalid stored TAT")
        end
    else
        local legacy = tonumber(stored)
        if not legacy or legacy < 0 then
            return redis.error_reply("invalid stored TAT")
        end
        tat_sec = math.floor(legacy / 1000000)
        tat_usec = legacy % 1000000
    end
    if lt(tat_sec, tat_usec, now_sec, now_usec) then
        tat_sec = now_sec
        tat_usec = now_usec
    end
end

if not lt(tat_sec, tat_usec, tol_sec, tol_usec) then
    local allow_sec, allow_usec = sub(tat_sec, tat_usec, tol_sec, tol_usec)
    if lt(now_sec, now_usec, allow_sec, allow_usec) then
        local retry_sec, retry_usec = sub(allow_sec, allow_usec, now_sec, now_usec)
        local reset_sec, reset_usec = sub(tat_sec, tat_usec, now_sec, now_usec)
        return {0, 0, retry_sec, retry_usec, reset_sec, reset_usec}
    end
end

local new_tat_sec, new_tat_usec = add(tat_sec, tat_usec, int_sec, int_usec)
local debt_sec, debt_usec = sub(new_tat_sec, new_tat_usec, now_sec, now_usec)
local remaining = 0
if not lt(tol_sec, tol_usec, debt_sec, debt_usec) then
    local head_sec, head_usec = sub(tol_sec, tol_usec, debt_sec, debt_usec)
    local q = math.floor((head_sec * 1000000 + head_usec) / (int_sec * 1000000 + int_usec))
    if q < 0 then
        q = 0
    end
    if q > burst - 1 then
        q = burst - 1
    end
    local ps, pu = mul(q, int_sec, int_usec)
    while q > 0 and lt(head_sec, head_usec, ps, pu) do
        q = q - 1
        ps, pu = mul(q, int_sec, int_usec)
    end
    while q + 1 <= burst - 1 do
        local ns, nu = add(ps, pu, int_sec, int_usec)
        if lt(head_sec, head_usec, ns, nu) then
            break
        end
        q = q + 1
        ps, pu = ns, nu
    end
    remaining = q + 1
    if remaining > burst - 1 then
        remaining = burst - 1
    end
end

local ttl_ms = debt_sec * 1000 + math.floor((debt_usec + 999) / 1000)
if ttl_ms < 1 then
    ttl_ms = 1
elseif ttl_ms > 9223372036854 then
    ttl_ms = 9223372036854
end
redis.call("SET", KEYS[1], string.format("%.0f:%.0f", new_tat_sec, new_tat_usec), "PX", ttl_ms)
return {1, remaining, 0, 0, debt_sec, debt_usec}
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
	// A literal prefix+key concatenation lets nested namespaces address the same
	// quota. Frame the complete configured prefix so Take and Reset share one
	// unambiguous namespace. Legacy keys are deliberately not read or removed.
	physicalPrefix := "__thinkgo_ratelimit__:v2:" + strconv.Itoa(len(prefix)) + ":" + prefix + ":"
	return &RedisStore{client: client, prefix: physicalPrefix, opTimeout: opTimeout}, nil
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
		intervalMicros/1_000_000,
		intervalMicros%1_000_000,
		toleranceMicros/1_000_000,
		toleranceMicros%1_000_000,
		limit.Burst,
	).Result()
	if err != nil {
		if contextErr := operationContext.Err(); contextErr != nil {
			return Result{}, contextErr
		}
		return Result{}, fmt.Errorf("redis 限流原子判定失败: %w", err)
	}
	values, ok := raw.([]interface{})
	if !ok || len(values) != 6 {
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
	retryAfter, err := saturatingDurationFromSecMicros(parsed[2], parsed[3])
	if err != nil {
		return Result{}, err
	}
	resetAfter, err := saturatingDurationFromSecMicros(parsed[4], parsed[5])
	if err != nil {
		return Result{}, err
	}
	return Result{
		Allowed:    parsed[0] == 1,
		Limit:      limit.Burst,
		Remaining:  int(parsed[1]),
		RetryAfter: retryAfter,
		ResetAfter: resetAfter,
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
	if burst <= 0 || int64(burst) > math.MaxInt64/intervalMicros {
		return 0, 0, ErrInvalidConfiguration
	}
	return intervalMicros, int64(burst-1) * intervalMicros, nil
}

func saturatingDurationFromSecMicros(seconds, microseconds int64) (time.Duration, error) {
	if seconds < 0 || microseconds < 0 || microseconds >= 1_000_000 {
		return 0, fmt.Errorf("%w: 时间分量越界", ErrInvalidStoreResponse)
	}
	const maxSeconds = math.MaxInt64 / int64(time.Second)
	const maxRemainderNanos = math.MaxInt64 % int64(time.Second)
	remainderNanos := microseconds * int64(time.Microsecond)
	if seconds > maxSeconds || (seconds == maxSeconds && remainderNanos > maxRemainderNanos) {
		return time.Duration(math.MaxInt64), nil
	}
	return time.Duration(seconds)*time.Second + time.Duration(remainderNanos), nil
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
