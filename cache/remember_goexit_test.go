package cache

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"testing/synctest"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// 验证实际等待者能区分 panic 与 Goexit，业务调用者恢复原 panic 后仍可重新加载该键。
func TestRememberPublishesOwnerExitToWaitingAlias(t *testing.T) {
	for _, mode := range []string{"legacy_nil", "modern_nil", "panic_value", "goexit"} {
		t.Run(mode, func(t *testing.T) {
			panicSetting := "panicnil=0"
			if mode == "legacy_nil" {
				panicSetting = "panicnil=1"
			}
			t.Setenv("GODEBUG", os.Getenv("GODEBUG")+","+panicSetting)
			synctest.Test(t, func(t *testing.T) {
				owner, alias := newIssue148Aliases(t, cacheDriver.NewMemory())
				ownerCtx, cancel := context.WithCancel(t.Context())
				defer cancel()
				entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				panicValue := errors.New("加载回调 panic")
				var recovered interface{}
				var returned, resumed bool
				go func() {
					defer close(done)
					func() {
						defer func() { recovered = recover() }()
						_, _ = owner.RememberContext(ownerCtx, "shared", time.Minute, func() (interface{}, error) {
							close(entered)
							<-release
							if mode == "goexit" {
								runtime.Goexit()
							}
							if mode == "panic_value" {
								panic(panicValue)
							}
							panic(nil)
						})
						returned = true
					}()
					resumed = true
				}()
				<-entered
				waiter := startRemember(t.Context(), alias, func() (interface{}, error) {
					t.Error("别名等待者重复执行了加载回调")
					return nil, nil
				})
				synctest.Wait()
				cancel()
				close(release)
				<-done
				wantError := "Remember 回调发生 panic"
				if mode == "goexit" {
					wantError = "Remember 回调异常退出"
				}
				if result := <-waiter; result.value != nil || result.err == nil || result.err.Error() != wantError {
					t.Errorf("等待者未收到加载回调的真实退出类型：value=%v err=%v，期望 %q", result.value, result.err, wantError)
				}
				if returned || resumed != (mode != "goexit") {
					t.Errorf("调用者的退出方式改变：returned=%v resumed=%v", returned, resumed)
				}
				switch mode {
				case "panic_value":
					if recovered != panicValue {
						t.Errorf("业务 panic 原值改变：%v", recovered)
					}
				case "modern_nil":
					if _, ok := recovered.(*runtime.PanicNilError); !ok {
						t.Errorf("默认 nil panic 类型改变：%T", recovered)
					}
				default:
					if recovered != nil {
						t.Errorf("恢复值应为 nil：%v", recovered)
					}
				}
				value, err := alias.Remember("shared", time.Minute, func() (interface{}, error) { return "重新加载", nil })
				if err != nil || value != "重新加载" {
					t.Errorf("退出后的同键加载失败：value=%v err=%v", value, err)
				}
			})
		})
	}
}

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
