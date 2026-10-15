//go:build windows

package winfile

import (
	"context"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// contentionContext reports when acquisition starts waiting, without changing
// cancellation or blocking the context methods.
type contentionContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *contentionContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

// TestGuardWaitingContenderProgress models a writer repeatedly entering short
// critical sections. A waiting caller must get an opportunity before its
// unchanged 15-second deadline; total owner load is bounded at 20 seconds.
func TestGuardWaitingContenderProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contended.guard")
	owner, err := openGuard(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	locked, err := tryGuardLock(owner)
	if err != nil || !locked {
		t.Fatalf("owner acquire: locked=%t err=%v", locked, err)
	}
	defer func() {
		if locked {
			_ = unlockGuard(owner)
		}
	}()
	base, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ctx := &contentionContext{Context: base, waiting: make(chan struct{})}
	done := make(chan error, 1)
	completed := false
	defer func() {
		cancel()
		if locked {
			_ = unlockGuard(owner)
			locked = false
		}
		if !completed {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("cancelled waiter did not exit")
			}
		}
	}()
	started := time.Now()
	t.Logf("GOMAXPROCS=%d CPUs=%d", runtime.GOMAXPROCS(0), runtime.NumCPU())
	go func() { done <- WithGuard(ctx, path, func() error { return nil }) }()
	select {
	case <-ctx.waiting:
	case err := <-done:
		completed = true
		t.Fatalf("waiter returned while owner held lock: %v", err)
	case <-base.Done():
		t.Fatal("waiter did not begin waiting")
	}
	deadline := time.Now().Add(20 * time.Second)
	reentries := 0
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			completed = true
			t.Logf("waiter elapsed=%s owner_reentries=%d result=%v", time.Since(started), reentries, err)
			if err != nil {
				t.Fatalf("waiting contender made no progress: %v", err)
			}
			return
		default:
		}
		// This delay is inside the held critical section, never an artificial
		// delay between release and re-entry.
		time.Sleep(125 * time.Millisecond)
		if err = unlockGuard(owner); err != nil {
			t.Fatal(err)
		}
		locked = false
		locked, err = tryGuardLock(owner)
		if err != nil {
			t.Fatal(err)
		}
		if !locked {
			select {
			case err := <-done:
				completed = true
				t.Logf("waiter elapsed=%s owner_reentries=%d result=%v", time.Since(started), reentries, err)
				if err != nil {
					t.Fatal(err)
				}
				return
			case <-base.Done():
				t.Fatalf("owner released but waiter did not finish: %v", base.Err())
			}
		}
		reentries++
	}
	t.Fatalf("bounded owner load ended without waiter completion: reentries=%d", reentries)
}
