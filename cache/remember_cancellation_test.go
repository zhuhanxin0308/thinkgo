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

func startRemember(ctx context.Context, store *Cache, load func() (interface{}, error)) <-chan issue148RememberResult {
	result := make(chan issue148RememberResult, 1)
	go func() {
		value, err := store.RememberContext(ctx, "shared", time.Minute, load)
		result <- issue148RememberResult{value: value, err: err}
	}()
	return result
}

func TestRememberCanceledOwnerWithHealthyAlias(t *testing.T) {
	for _, outcome := range []string{"success", "callback_error", "business_error", "closed"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ownerStore, alias := newIssue148Aliases(t, cacheDriver.NewMemory())
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				businessErr := errors.New("加载业务记录失败")
				entered, release := make(chan struct{}), make(chan struct{})
				owner := startRemember(ctx, ownerStore, func() (interface{}, error) {
					close(entered)
					<-release
					if outcome == "callback_error" {
						// 回调可以捕获业务请求的上下文，它返回的具体错误应传给健康等待者。
						return nil, ctx.Err()
					}
					if outcome == "business_error" {
						return nil, businessErr
					}
					return "loaded", nil
				})
				<-entered
				follower := startRemember(t.Context(), alias, func() (interface{}, error) {
					return nil, errors.New("健康等待者重复调用了加载回调")
				})
				synctest.Wait()
				cancel()
				if outcome == "closed" {
					if err := ownerStore.Close(); err != nil {
						t.Fatal(err)
					}
				}
				close(release)
				first, second := <-owner, <-follower
				if outcome == "business_error" {
					if first.err != businessErr || second.err != businessErr {
						t.Errorf("业务错误未原样传回两个调用：%v %v", first.err, second.err)
					}
				} else if !errors.Is(first.err, context.Canceled) {
					t.Errorf("首个调用未得到自身的取消错误：%v", first.err)
				}
				switch outcome {
				case "success":
					if second.value != "loaded" || second.err != nil {
						t.Errorf("健康调用丢失共享结果：%#v", second)
					}
					if value, found, err := alias.Get("shared"); err != nil || !found || value != "loaded" {
						t.Errorf("共享结果未写入别名共用的缓存：%v %t %v", value, found, err)
					}
				case "callback_error":
					if second.err != context.Canceled {
						t.Errorf("回调错误被替换：%v", second.err)
					}
					if value, err := alias.Remember("shared", time.Minute, func() (interface{}, error) { return "retry", nil }); err != nil || value != "retry" {
						t.Errorf("回调失败后无法重新加载：%v %v", value, err)
					}
				case "closed":
					if !errors.Is(second.err, ErrCacheClosed) {
						t.Errorf("回调期间关闭的缓存报告了错误的提交结果：%v", second.err)
					}
				}
			})
		})
	}
}

func TestRememberWaitsForAbandonedCallbackBeforeReload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 仅公开旧驱动接口，确认不支持上下文的后端也不会收到迟到的写回。
		store := NewCache(nil, struct{ Driver }{cacheDriver.NewMemory()})
		t.Cleanup(func() { _ = store.Close() })
		ownerCtx, cancelOwner := context.WithCancel(t.Context())
		defer cancelOwner()
		firstEntered, firstRelease := make(chan struct{}), make(chan struct{})
		first := startRemember(ownerCtx, store, func() (interface{}, error) {
			close(firstEntered)
			<-firstRelease
			return "abandoned", nil
		})
		<-firstEntered
		cancelOwner()
		nextEntered, nextRelease := make(chan struct{}), make(chan struct{})
		next := startRemember(t.Context(), store, func() (interface{}, error) {
			close(nextEntered)
			<-nextRelease
			return "new", nil
		})
		synctest.Wait()
		select {
		case <-nextEntered:
			t.Error("新回调与已取消但未退出的回调并行执行")
		default:
		}
		close(firstRelease)
		old := <-first
		synctest.Wait()
		if !errors.Is(old.err, context.Canceled) {
			t.Errorf("已取消的旧调用返回：%#v", old)
		}
		if value, found, err := store.Get("shared"); err != nil || found {
			t.Errorf("最后一个订阅者取消后仍然写入：%v %t %v", value, found, err)
		}
		close(nextRelease)
		if got := <-next; got.value != "new" || got.err != nil {
			t.Errorf("健康新调用未独立加载：%#v", got)
		}
	})
}

type rememberBlockingBackend struct {
	Driver
	stage   string
	reads   atomic.Int32
	entered chan context.Context
	release chan struct{}
}

func (backend *rememberBlockingBackend) wait(ctx context.Context) error {
	backend.entered <- ctx
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-backend.release:
		return nil
	}
}

func (backend *rememberBlockingBackend) GetContext(ctx context.Context, key string) (interface{}, bool, error) {
	if key == "shared" && backend.stage == "read" && backend.reads.Add(1) == 2 {
		if err := backend.wait(ctx); err != nil {
			return nil, false, err
		}
	}
	return backend.Get(key)
}

func (backend *rememberBlockingBackend) SetContext(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	if key == "shared" && backend.stage == "write" {
		if err := backend.wait(ctx); err != nil {
			return err
		}
	}
	return backend.Set(key, value, ttl)
}

func TestRememberBackendContextFollowsSubscribers(t *testing.T) {
	for _, stage := range []string{"read", "write"} {
		for _, subscribers := range []string{"healthy_follower", "all_canceled"} {
			t.Run(stage+"/"+subscribers, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					backend := &rememberBlockingBackend{
						Driver: cacheDriver.NewMemory(), stage: stage,
						entered: make(chan context.Context, 1), release: make(chan struct{}),
					}
					store := NewCache(nil, backend)
					t.Cleanup(func() { _ = store.Close() })
					ownerCtx, cancelOwner := context.WithCancel(context.WithValue(t.Context(), cacheContextKey{}, "trace"))
					defer cancelOwner()
					followerCtx, cancelFollower := context.WithCancel(t.Context())
					defer cancelFollower()
					calls := 0
					owner := startRemember(ownerCtx, store, func() (interface{}, error) {
						calls++
						return "loaded", nil
					})
					work := <-backend.entered
					follower := startRemember(followerCtx, store, func() (interface{}, error) {
						return nil, errors.New("重复执行了回调")
					})
					synctest.Wait()
					cancelOwner()
					if subscribers == "all_canceled" {
						cancelFollower()
					}
					synctest.Wait()
					if work.Value(cacheContextKey{}) != "trace" || (work.Err() != nil) != (subscribers == "all_canceled") {
						t.Errorf("后端未能同时保留请求值和订阅者的生命周期：%v %v", work.Value(cacheContextKey{}), work.Err())
					}
					close(backend.release)
					first, second := <-owner, <-follower
					if !errors.Is(first.err, context.Canceled) {
						t.Errorf("首个调用未返回自身取消：%v", first.err)
					}
					if subscribers == "all_canceled" {
						if !errors.Is(second.err, context.Canceled) || (stage == "read" && calls != 0) {
							t.Errorf("最后的订阅取消后继续了加载：%v，回调调用 %d 次", second.err, calls)
						}
						if value, found, err := backend.Get("shared"); err != nil || found {
							t.Errorf("后端保留了无人等待的结果：%v %t %v", value, found, err)
						}
					} else if second.err != nil || second.value != "loaded" || calls != 1 {
						t.Errorf("保留健康订阅时未完成一次加载：%#v，回调调用 %d 次", second, calls)
					}
				})
			})
		}
	}
}
