package cache

import (
	"crypto/rand"
	"io"
	"math/big"
	"time"
)

const maximumRememberRetry = 100 * time.Millisecond

// rememberRetryDelay 使用等幅抖动分散竞争者；随机数不用于锁 owner 或其他安全凭据。
func rememberRetryDelay(ceiling, remaining time.Duration) time.Duration {
	return rememberRetryDelayFrom(rand.Reader, ceiling, remaining)
}

// rememberRetryDelayFrom 将熵读取限制在竞争路径；读取失败时保持有界退避，
// 不将调度随机数的错误传播为锁错误，也不回退到高频轮询。
func rememberRetryDelayFrom(entropy io.Reader, ceiling, remaining time.Duration) time.Duration {
	if remaining <= 0 {
		return 0
	}
	if ceiling < lockRetryInterval {
		ceiling = lockRetryInterval
	}
	if ceiling > maximumRememberRetry {
		ceiling = maximumRememberRetry
	}
	lower := ceiling / 2
	delay := ceiling
	if jitter, err := rand.Int(entropy, big.NewInt(int64(ceiling-lower)+1)); err == nil {
		delay = lower + time.Duration(jitter.Int64())
	}
	if delay > remaining {
		return remaining
	}
	return delay
}
