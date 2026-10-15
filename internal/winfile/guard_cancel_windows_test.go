//go:build windows

package winfile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The native implementation first observes Done when registering AfterFunc
// after LockFileEx returns ERROR_IO_PENDING. Tests observe the original context
// themselves so they cannot accidentally signal this submission handshake.
type guardPendingContext struct {
	context.Context
	pending chan struct{}
	once    sync.Once
}

func (ctx *guardPendingContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.pending) })
	return ctx.Context.Done()
}

type cancelGuardOwner struct {
	releaseOnce sync.Once
	releaseChan chan struct{}
	done        chan struct{}
	err         error
}

func (owner *cancelGuardOwner) release() { owner.releaseOnce.Do(func() { close(owner.releaseChan) }) }

func startCancelGuardOwner(t *testing.T, path string) *cancelGuardOwner {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	owner := &cancelGuardOwner{releaseChan: make(chan struct{}), done: make(chan struct{})}
	acquired := make(chan struct{})
	go func() {
		owner.err = WithGuard(ctx, path, func() error {
			close(acquired)
			<-owner.releaseChan
			return nil
		})
		close(owner.done)
	}()
	t.Cleanup(func() {
		cancel()
		owner.release()
		waitCancelGuardDone(t, owner.done, "owner cleanup")
	})
	select {
	case <-acquired:
	case <-owner.done:
		t.Fatalf("owner did not acquire: %v", owner.err)
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not acquire within fixture budget")
	}
	return owner
}

type cancelGuardWaiter struct {
	done  chan struct{}
	err   error
	calls atomic.Uint32
}

func startCancelGuardWaiter(t *testing.T, path string, base context.Context, cancel context.CancelFunc, releaseOnCleanup func()) *cancelGuardWaiter {
	t.Helper()
	ctx := &guardPendingContext{Context: base, pending: make(chan struct{})}
	waiter := &cancelGuardWaiter{done: make(chan struct{})}
	go func() {
		waiter.err = WithGuard(ctx, path, func() error {
			waiter.calls.Add(1)
			return nil
		})
		close(waiter.done)
	}()
	t.Cleanup(func() {
		cancel()
		// Release the independent owner even on an assertion failure, allowing
		// cleanup to join a waiter whose cancellation implementation is broken.
		releaseOnCleanup()
		waitCancelGuardDone(t, waiter.done, "waiter cleanup")
	})
	select {
	case <-ctx.pending:
	case <-waiter.done:
		select {
		case <-ctx.pending:
			// Cancellation may complete before the test goroutine is scheduled.
		default:
			t.Fatalf("waiter returned before pending submission: %v", waiter.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not submit pending request")
	}
	return waiter
}

func waitCancelGuardDone(t *testing.T, done <-chan struct{}, phase string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not complete", phase)
	}
}

func assertCancelGuardOwnerStillHeld(t *testing.T, path string) {
	t.Helper()
	probe, err := openGuard(path)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	locked, err := tryGuardLock(probe)
	if locked {
		_ = unlockGuard(probe)
	}
	if err != nil || locked {
		t.Fatalf("canceling waiter changed the independent owner's lock: locked=%t err=%v", locked, err)
	}
}

func assertCancelGuardReacquiresAndCloses(t *testing.T, path string, before os.FileInfo) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	called := false
	if err := WithGuard(ctx, path, func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("completed cancellation leaked a lock: called=%t err=%v", called, err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("cancellation changed guard identity: %v", err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	// Every guard handle omits FILE_SHARE_DELETE. DELETE access can open only
	// once all owner, waiter and fresh-acquisition guard handles have closed.
	probe, err := windows.CreateFile(name, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatalf("completed operation leaked a guard handle: %v", err)
	}
	if err := windows.CloseHandle(probe); err != nil {
		t.Fatal(err)
	}
}

func TestGuardPendingCancellationReleasesRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cancel.guard")
	owner := startCancelGuardOwner(t, path)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 32 {
		var base context.Context
		var cancel context.CancelFunc
		want := context.Canceled
		if attempt%8 == 7 {
			base, cancel = context.WithTimeout(t.Context(), 2*time.Second)
			want = context.DeadlineExceeded
		} else {
			base, cancel = context.WithCancel(t.Context())
		}
		waiter := startCancelGuardWaiter(t, path, base, cancel, owner.release)
		if want == context.Canceled {
			cancel()
		}
		waitCancelGuardDone(t, waiter.done, fmt.Sprintf("pending cancellation %d", attempt))
		cancel()
		if !errors.Is(waiter.err, want) || waiter.calls.Load() != 0 {
			t.Fatalf("attempt=%d callback=%d err=%v want=%v", attempt, waiter.calls.Load(), waiter.err, want)
		}
		assertCancelGuardOwnerStillHeld(t, path)
	}
	owner.release()
	waitCancelGuardDone(t, owner.done, "owner release after repeated cancellation")
	if owner.err != nil {
		t.Fatal(owner.err)
	}
	assertCancelGuardReacquiresAndCloses(t, path, before)
}

func TestGuardPendingGrantCancellationRace(t *testing.T) {
	var granted, canceled int
	for attempt := range 32 {
		t.Run(fmt.Sprintf("race-%02d", attempt), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "grant-cancel.guard")
			owner := startCancelGuardOwner(t, path)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			waiter := startCancelGuardWaiter(t, path, ctx, cancel, owner.release)
			start := make(chan struct{})
			var racers sync.WaitGroup
			racers.Go(func() { <-start; owner.release() })
			racers.Go(func() { <-start; cancel() })
			close(start)
			racers.Wait()
			waitCancelGuardDone(t, waiter.done, "grant/cancel waiter")
			waitCancelGuardDone(t, owner.done, "grant/cancel owner")
			if owner.err != nil {
				t.Fatal(owner.err)
			}
			switch {
			case waiter.err == nil && waiter.calls.Load() == 1:
				granted++
			case errors.Is(waiter.err, context.Canceled) && waiter.calls.Load() == 0:
				canceled++
			default:
				t.Fatalf("invalid grant/cancel outcome: callback=%d err=%v", waiter.calls.Load(), waiter.err)
			}
			assertCancelGuardReacquiresAndCloses(t, path, before)
		})
	}
	// Both outcomes are legal. This schedule does not promise to execute both.
	t.Logf("grant/cancel outcomes: granted=%d canceled=%d", granted, canceled)
}

func TestGuardCancellationDoesNotReleaseActiveCallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.guard")
	ctx, cancel := context.WithCancel(t.Context())
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	var result error
	go func() {
		result = WithGuard(ctx, path, func() error {
			close(started)
			<-release
			return nil
		})
		close(done)
	}()
	t.Cleanup(func() { cancel(); finish(); waitCancelGuardDone(t, done, "active callback cleanup") })
	select {
	case <-started:
	case <-done:
		t.Fatalf("callback did not start: %v", result)
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not acquire")
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	assertCancelGuardOwnerStillHeld(t, path)
	// Keep the callback blocked while another real pending request reaches its
	// deadline. An asynchronous cancellation callback cannot release the owner
	// unnoticed merely because the first nonblocking probe ran before it did.
	probeContext, cancelProbe := context.WithTimeout(t.Context(), 2*time.Second)
	probe := startCancelGuardWaiter(t, path, probeContext, cancelProbe, finish)
	waitCancelGuardDone(t, probe.done, "active callback exclusion probe")
	cancelProbe()
	if !errors.Is(probe.err, context.DeadlineExceeded) || probe.calls.Load() != 0 {
		t.Fatalf("cancellation released an active callback: callback=%d err=%v", probe.calls.Load(), probe.err)
	}
	assertCancelGuardOwnerStillHeld(t, path)
	select {
	case <-done:
		t.Fatal("cancellation returned before active callback completed")
	default:
	}
	finish()
	waitCancelGuardDone(t, done, "active callback completion")
	if result != nil {
		t.Fatalf("cancellation retroactively failed completed callback: %v", result)
	}
	assertCancelGuardReacquiresAndCloses(t, path, before)
}
