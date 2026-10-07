package cache

import (
	"math/rand/v2"
	"time"
)

const maximumRememberRetry = 100 * time.Millisecond

// rememberRetryDelay 使用等幅抖动分散竞争者；随机数不用于锁 owner 或其他安全凭据。
func rememberRetryDelay(ceiling, remaining time.Duration) time.Duration {
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
	delay := lower + time.Duration(rand.Int64N(int64(ceiling-lower)+1))
	if delay > remaining {
		return remaining
	}
	return delay
}
