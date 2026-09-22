// Package ratelimit 提供与传输层解耦的原子限流存储契约和进程内实现。
package ratelimit

import (
	"context"
	"errors"
	"math"
	"time"
)

const (
	maximumRatePerPeriod = 1_000_000_000
	maximumBurst         = 1_000_000
	maximumKeyBytes      = 1024
)

var (
	// ErrInvalidConfiguration 表示速率、周期、突发量或存储容量不可执行。
	ErrInvalidConfiguration = errors.New("限流配置非法")
	// ErrInvalidKey 表示请求没有稳定且有界的限流键。
	ErrInvalidKey = errors.New("限流键非法")
	// ErrStoreCapacity 表示不受信任键空间已达到硬上限。
	ErrStoreCapacity = errors.New("限流存储容量已满")
	// ErrStoreClosed 表示进程内存储已经释放，不再接受新的判定。
	ErrStoreClosed = errors.New("限流存储已关闭")
	// ErrInvalidStoreResponse 表示共享存储返回了不符合原子限流协议的数据。
	ErrInvalidStoreResponse = errors.New("限流存储响应非法")
)

// Limit 使用 GCRA 描述稳定速率和可接受的瞬时突发量。
type Limit struct {
	Rate   int
	Period time.Duration
	Burst  int
}

// Result 是一次原子额度判定的完整快照。
type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
	ResetAfter time.Duration
}

// Store 是可替换为 Redis 等共享后端的原子额度存储契约。
type Store interface {
	Take(context.Context, string, Limit, time.Time) (Result, error)
}

// Validate 校验限流策略并返回相邻请求的理论发射间隔。
func Validate(limit Limit) (time.Duration, error) {
	if limit.Rate <= 0 || limit.Rate > maximumRatePerPeriod || limit.Period <= 0 || limit.Burst <= 0 || limit.Burst > maximumBurst {
		return 0, ErrInvalidConfiguration
	}
	interval := limit.Period / time.Duration(limit.Rate)
	if interval <= 0 {
		return 0, ErrInvalidConfiguration
	}
	// 除了容差 (Burst-1)*interval，连续接受 Burst 次后的 TAT
	// 还会达到 now+Burst*interval，两个乘法都必须可表示。
	if int64(limit.Burst) > math.MaxInt64/int64(interval) {
		return 0, ErrInvalidConfiguration
	}
	return interval, nil
}

func positiveDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}
