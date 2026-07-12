package driver

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
	// ErrUnsafeCacheEntry 表示缓存项不是根目录内的普通文件。
	ErrUnsafeCacheEntry = errors.New("文件缓存项不安全")
	// ErrCacheEntryTooLarge 表示磁盘缓存项超过读取上限。
	ErrCacheEntryTooLarge = errors.New("文件缓存项超过大小上限")
	// ErrCorruptCacheEntry 表示缓存文件内容无法按受支持格式解码。
	ErrCorruptCacheEntry = errors.New("文件缓存项损坏")
	// ErrInvalidCacheLock 表示锁 owner 或 TTL 无效、锁文件损坏。
	ErrInvalidCacheLock = errors.New("缓存锁无效")
	// ErrInvalidRedisConfig 表示 Redis 驱动配置类型或范围错误。
	ErrInvalidRedisConfig = errors.New("Redis 缓存配置非法")
	// ErrInvalidRedisClient 表示 Redis 驱动没有可用客户端。
	ErrInvalidRedisClient = errors.New("Redis 缓存客户端不可用")
	// ErrUnsafeRedisFlush 表示空前缀 Redis 未授权执行 FLUSHDB。
	ErrUnsafeRedisFlush = errors.New("Redis 空前缀清空未授权")
	// ErrInvalidCacheDatabase 表示 DB 缓存连接不可用。
	ErrInvalidCacheDatabase = errors.New("DB 缓存连接非法")
	// ErrInvalidCacheTable 表示 DB 缓存表名不安全。
	ErrInvalidCacheTable = errors.New("DB 缓存表名非法")
	// ErrCacheKeyTooLong 表示驱动后端无法存储该长度的键。
	ErrCacheKeyTooLong = errors.New("缓存键超过驱动上限")
)

func validateDriverTTL(ttl time.Duration) error {
	if ttl < 0 {
		return fmt.Errorf("%w: %s", ErrInvalidDriverTTL, ttl)
	}
	return nil
}

func containsControlCharacter(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func validateCounterStep(step int64) error {
	if step < 0 {
		return fmt.Errorf("%w: %d", ErrInvalidCounterStep, step)
	}
	return nil
}

func strictCounterValue(value interface{}) (int64, error) {
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

// decodeCounterJSON 使用 json.Number 保留整数精度，避免 MaxInt64 经 float64 舍入后被误判。
func decodeCounterJSON(encoded []byte) (interface{}, error) {
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

func checkedCounterAdd(value, step int64) (int64, error) {
	if step > 0 && value > math.MaxInt64-step || step < 0 && value < math.MinInt64-step {
		return 0, ErrCounterOverflow
	}
	return value + step, nil
}

func checkedCounterSubtract(value, step int64) (int64, error) {
	if step < 0 {
		return 0, ErrInvalidCounterStep
	}
	if value < math.MinInt64+step {
		return 0, ErrCounterOverflow
	}
	return value - step, nil
}
