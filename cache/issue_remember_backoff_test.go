package cache

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

func TestIssue46DelayBoundsAndBudget(t *testing.T) {
	for _, ceiling := range []time.Duration{-1, 0, time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond, 80 * time.Millisecond, time.Second, time.Duration(1<<63 - 1)} {
		upper := min(max(ceiling, lockRetryInterval), maximumRememberRetry)
		for _, remaining := range []time.Duration{-1, 0, time.Nanosecond, 3 * time.Millisecond, time.Minute} {
			for n := 0; n < 100; n++ {
				got := rememberRetryDelay(ceiling, remaining)
				if remaining <= 0 {
					if got != 0 {
						t.Fatal("expired budget still waits")
					}
				} else if got < min(upper/2, remaining) || got > min(upper, remaining) {
					t.Fatalf("ceiling=%v remaining=%v delay=%v", ceiling, remaining, got)
				}
			}
		}
	}
}

type issue46ContendedDriver struct {
	*cacheDriver.Memory
	attempts atomic.Int64
	failure  error
}

func (d *issue46ContendedDriver) AcquireLock(string, string, time.Duration) (bool, error) {
	d.attempts.Add(1)
	return false, d.failure
}

func TestIssue46ContentionDeadlineAndAttemptBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := &issue46ContendedDriver{Memory: cacheDriver.NewMemory()}
		manager := NewCache(nil, backend)
		defer manager.Close()
		start := time.Now()
		_, err := manager.RememberWithLock("busy", time.Minute, time.Minute, func() (interface{}, error) {
			t.Fatal("busy lock executed callback")
			return nil, nil
		})
		if !errors.Is(err, ErrCacheLockBusy) || time.Since(start) != rememberLockWait {
			t.Fatalf("deadline=%v err=%v", time.Since(start), err)
		}
		// Equal jitter at the documented ceilings gives a deterministic upper bound,
		// regardless of the random values. Fixed 5ms polling makes ~1000 attempts.
		if attempts := backend.attempts.Load(); attempts < 1 || attempts > 110 {
			t.Fatalf("retry amplification: %d attempts", attempts)
		}
	})
}

func TestIssue46CancellationAndBackendError(t *testing.T) {
	for _, mode := range []string{"already_cancelled", "cancel_during_wait", "deadline", "backend_error"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				backend := &issue46ContendedDriver{Memory: cacheDriver.NewMemory()}
				manager := NewCache(nil, backend)
				defer manager.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				want := error(context.Canceled)
				switch mode {
				case "already_cancelled":
					cancel()
				case "cancel_during_wait":
					go func() { time.Sleep(250 * time.Millisecond); cancel() }()
				case "deadline":
					var deadlineCancel context.CancelFunc
					ctx, deadlineCancel = context.WithTimeout(ctx, 250*time.Millisecond)
					defer deadlineCancel()
					want = context.DeadlineExceeded
				case "backend_error":
					backend.failure = errors.New("backend unavailable")
					want = backend.failure
				}
				_, err := manager.RememberWithLockContext(ctx, "busy", time.Minute, time.Minute, func() (interface{}, error) {
					t.Fatal("failed lock executed callback")
					return nil, nil
				})
				if !errors.Is(err, want) {
					t.Fatalf("err=%v want=%v", err, want)
				}
				attempts := backend.attempts.Load()
				if (mode == "already_cancelled" && attempts != 0) || (mode == "backend_error" && attempts != 1) || attempts > 12 {
					t.Fatalf("unexpected retry count: %d", attempts)
				}
			})
		})
	}
}

func TestIssue46UncontendedLockHasNoBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := NewCache(nil, cacheDriver.NewMemory())
		defer manager.Close()
		start := time.Now()
		called := 0
		value, err := manager.RememberWithLock("ready", time.Minute, time.Minute, func() (interface{}, error) { called++; return "ready", nil })
		if err != nil || value != "ready" || called != 1 || !time.Now().Equal(start) {
			t.Fatalf("value=%v err=%v called=%d elapsed=%v", value, err, called, time.Since(start))
		}
	})
}
