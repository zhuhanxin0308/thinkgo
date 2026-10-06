package cache

import (
	"context"
	"runtime"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// TestRememberGoexitPublishesFailure verifies cleanup before the loading goroutine terminates.
func TestRememberGoexitPublishesFailure(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	defer func() { _ = manager.Close() }()
	entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		_, _ = manager.Remember("abandoned", time.Minute, func() (interface{}, error) {
			close(entered)
			<-release
			runtime.Goexit()
			return nil, nil
		})
	}()
	<-entered
	key := manager.storeName + "\x00abandoned"
	manager.state.rememberMu.Lock()
	call := manager.state.remembering[key]
	manager.state.rememberMu.Unlock()
	if call == nil {
		close(release)
		t.Fatal("missing active shared call")
	}
	close(release)
	<-exited
	// done is the publication boundary all coalesced waiters use.
	select {
	case <-call.done:
		if call.err == nil || call.value != nil {
			t.Errorf("abandoned load reported success: value=%v err=%v", call.value, call.err)
		}
	default:
		t.Error("Goexit did not notify shared-load waiters")
	}
	manager.state.rememberMu.Lock()
	remaining := manager.state.remembering[key] != nil
	manager.state.rememberMu.Unlock()
	if remaining {
		t.Error("Goexit permanently retained the shared-load key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	value, err := manager.RememberContext(ctx, "abandoned", time.Minute, func() (interface{}, error) { return "recovered", nil })
	if err != nil || value != "recovered" {
		t.Errorf("subsequent load could not recover: value=%v err=%v", value, err)
	}
}
