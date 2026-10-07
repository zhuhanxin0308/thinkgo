package cache

import (
	"bytes"
	"errors"
	"io"
	"math/big"
	"testing"
	"time"
)

// TestRememberRetryEntropyBounds 使用确定性的熵覆盖区间两端，而非依靠抽样碰中边界。
func TestRememberRetryEntropyBounds(t *testing.T) {
	for _, ceiling := range []time.Duration{-1, 0, time.Millisecond, 5 * time.Millisecond, 80 * time.Millisecond, time.Second, time.Duration(1<<63 - 1)} {
		upper := min(max(ceiling, lockRetryInterval), maximumRememberRetry)
		lower := upper / 2
		last := big.NewInt(int64(upper - lower))
		width := (last.BitLen() + 7) / 8
		for _, endpoint := range []int64{0, last.Int64()} {
			for _, remaining := range []time.Duration{time.Nanosecond, 3 * time.Millisecond, time.Minute} {
				entropy := big.NewInt(endpoint).FillBytes(make([]byte, width))
				got := rememberRetryDelayFrom(bytes.NewReader(entropy), ceiling, remaining)
				want := min(lower+time.Duration(endpoint), remaining)
				if got != want {
					t.Fatalf("ceiling=%v remaining=%v endpoint=%d: got %v want %v", ceiling, remaining, endpoint, got, want)
				}
			}
		}
	}
}

type rememberRetryFailedEntropy struct{ reads int }

func (r *rememberRetryFailedEntropy) Read([]byte) (int, error) {
	r.reads++
	return 0, errors.New("entropy unavailable")
}

// TestRememberRetryEntropyFailure 保证错误及短读都不把退避降为零，也不延长剩余预算。
func TestRememberRetryEntropyFailure(t *testing.T) {
	for _, remaining := range []time.Duration{time.Nanosecond, 10 * time.Millisecond, time.Minute} {
		for _, entropy := range []io.Reader{&rememberRetryFailedEntropy{}, bytes.NewReader(nil), bytes.NewReader([]byte{0})} {
			got := rememberRetryDelayFrom(entropy, maximumRememberRetry, remaining)
			if want := min(maximumRememberRetry, remaining); got != want {
				t.Fatalf("remaining=%v: got %v want %v", remaining, got, want)
			}
		}
	}
	for _, remaining := range []time.Duration{-time.Second, 0} {
		entropy := &rememberRetryFailedEntropy{}
		if got := rememberRetryDelayFrom(entropy, maximumRememberRetry, remaining); got != 0 || entropy.reads != 0 {
			t.Fatalf("expired budget: delay=%v entropy reads=%d", got, entropy.reads)
		}
	}
}
