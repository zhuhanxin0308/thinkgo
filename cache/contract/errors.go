// Package contract 定义缓存核心与驱动共享的错误和数值约束，不依赖任何具体后端。
package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"
	"unicode"
)

// FenceMetadataPrefix 标记 Clear 必须保留的单调代际元数据。
const FenceMetadataPrefix = "__thinkgo_cache_fence__:"

var (
	// ErrInvalidCounterValue 表示已有缓存值不是可精确转换的 int64。
	ErrInvalidCounterValue = errors.New("缓存计数值不是有效整数")
	// ErrCounterOverflow 表示计数运算超出 int64 范围。
	ErrCounterOverflow = errors.New("缓存计数溢出")
	// ErrInvalidCounterStep 表示驱动收到负计数步长。
	ErrInvalidCounterStep = errors.New("缓存计数步长非法")
	// ErrInvalidDriverTTL 表示驱动收到负 TTL。
	ErrInvalidDriverTTL = errors.New("缓存驱动 TTL 非法")
	// ErrInvalidCachePath 表示文件缓存根目录不可用。
	ErrInvalidCachePath = errors.New("文件缓存目录非法")
	// ErrInvalidMemoryCapacity 表示内存缓存容量配置为负数。
	ErrInvalidMemoryCapacity = errors.New("内存缓存容量非法")
	// ErrMemoryCapacityExhausted 表示有界内存缓存没有可安全淘汰的普通条目。
	ErrMemoryCapacityExhausted = errors.New("内存缓存容量已耗尽")
	// ErrUnsafeCacheEntry 表示缓存项不是根目录内的普通文件。
	ErrUnsafeCacheEntry = errors.New("文件缓存项不安全")
	// ErrCacheEntryTooLarge 表示磁盘缓存项超过读取上限。
	ErrCacheEntryTooLarge = errors.New("文件缓存项超过大小上限")
	// ErrCorruptCacheEntry 表示缓存文件内容无法按受支持格式解码。
	ErrCorruptCacheEntry = errors.New("文件缓存项损坏")
	// ErrInvalidCacheLock 表示锁 owner 或 TTL 无效、锁文件损坏。
	ErrInvalidCacheLock = errors.New("缓存锁无效")
	// ErrCacheLockBusy 表示在限定窗口内未能获得缓存锁。
	ErrCacheLockBusy = errors.New("缓存锁忙")
	// ErrCacheLockLost 表示临界区结束时锁已经过期或被替换。
	ErrCacheLockLost = errors.New("缓存锁已丢失")
	// ErrInvalidRedisConfig 表示 Redis 驱动配置类型或范围错误。
	ErrInvalidRedisConfig = errors.New("缓存 Redis 配置非法")
	// ErrInvalidRedisClient 表示 Redis 驱动没有可用客户端。
	ErrInvalidRedisClient = errors.New("缓存 Redis 客户端不可用")
	// ErrInvalidRedisContext 表示 Redis 缓存操作上下文为空。
	ErrInvalidRedisContext = errors.New("缓存 Redis 上下文无效")
	// ErrUnsafeRedisFlush 表示空前缀 Redis 未授权执行 FLUSHDB。
	ErrUnsafeRedisFlush = errors.New("缓存 Redis 空前缀清空未授权")
	// ErrInvalidCacheDatabase 表示 DB 缓存连接不可用。
	ErrInvalidCacheDatabase = errors.New("数据库缓存连接非法")
	// ErrInvalidCacheTable 表示 DB 缓存表名不安全。
	ErrInvalidCacheTable = errors.New("数据库缓存表名非法")
	// ErrCacheKeyTooLong 表示驱动后端无法存储该长度的键。
	ErrCacheKeyTooLong = errors.New("缓存键超过驱动上限")
	// ErrCacheBatchTooLarge 表示驱动批量操作超过资源预算。
	ErrCacheBatchTooLarge = errors.New("缓存批量操作过大")
	// ErrNilAtomicUpdate 表示原子更新没有提供计算回调。
	ErrNilAtomicUpdate = errors.New("缓存原子更新回调为空")
	// ErrAtomicUpdateConflict 表示乐观事务在有限重试后仍持续冲突。
	ErrAtomicUpdateConflict = errors.New("缓存原子更新冲突")
	// ErrInvalidCachePrefix 表示按前缀清理收到空前缀。
	ErrInvalidCachePrefix = errors.New("缓存清理前缀非法")
	// ErrUnscopedCacheEntry 表示旧文件项没有记录逻辑键，无法安全归属到某个前缀。
	ErrUnscopedCacheEntry = errors.New("文件缓存项缺少命名空间信息")
)

// ValidateDriverTTL 拒绝负有效期。
func ValidateDriverTTL(ttl time.Duration) error {
	if ttl < 0 {
		return fmt.Errorf("%w: %s", ErrInvalidDriverTTL, ttl)
	}
	return nil
}

// ContainsControlCharacter 检查配置或键中是否包含控制字符。
func ContainsControlCharacter(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

// ValidateCounterStep 拒绝负计数步长。
func ValidateCounterStep(step int64) error {
	if step < 0 {
		return fmt.Errorf("%w: %d", ErrInvalidCounterStep, step)
	}
	return nil
}

// StrictCounterValue 无损读取 int64 计数值，拒绝字符串、非整数与溢出。
func StrictCounterValue(value interface{}) (int64, error) {
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
		return strictUintCounter(uint64(typed))
	case uint8:
		return int64(typed), nil
	case uint16:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		return strictUintCounter(typed)
	case float32:
		return strictFloatCounter(float64(typed))
	case float64:
		return strictFloatCounter(typed)
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", ErrInvalidCounterValue, typed.String())
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("%w: %T", ErrInvalidCounterValue, value)
	}
}

// DecodeCounterJSON 使用 json.Number 保留整数精度，避免 MaxInt64 经 float64 舍入后被误判。
func DecodeCounterJSON(encoded []byte) (interface{}, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("缓存计数值包含多个 JSON 值")
		}
		return nil, err
	}
	return value, nil
}

func strictUintCounter(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, ErrInvalidCounterValue
	}
	return int64(value), nil
}

func strictFloatCounter(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
		value < float64(math.MinInt64) || value >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("%w: %v", ErrInvalidCounterValue, value)
	}
	return int64(value), nil
}

// CheckedCounterAdd 在计算前检查 int64 两端溢出。
func CheckedCounterAdd(value, step int64) (int64, error) {
	if step > 0 && value > math.MaxInt64-step || step < 0 && value < math.MinInt64-step {
		return 0, ErrCounterOverflow
	}
	return value + step, nil
}

// CheckedCounterSubtract 拒绝负递减步长并检查下溢。
func CheckedCounterSubtract(value, step int64) (int64, error) {
	if step < 0 {
		return 0, ErrInvalidCounterStep
	}
	if value < math.MinInt64+step {
		return 0, ErrCounterOverflow
	}
	return value - step, nil
}
