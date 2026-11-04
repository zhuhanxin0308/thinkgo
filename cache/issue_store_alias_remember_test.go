package cache

import (
	"context"
	"sync"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// issue148WaitContext 报告等待取消信号的同步点，不读取缓存的私有加载组。
type issue148WaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *issue148WaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

type issue148RememberResult struct {
	value interface{}
	err   error
}

func TestStoreAliasesCoalesceRememberWithoutMergingOtherStores(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "default-to-named"
		if reverse {
			name = "named-to-default"
		}
		t.Run(name, func(t *testing.T) {
			backend := cacheDriver.NewMemory()
			manager, named := newIssue148Aliases(t, backend)
			if err := manager.RegisterStore("other", cacheDriver.NewMemory()); err != nil {
				t.Fatal(err)
			}
			other, err := manager.Store("other")
			if err != nil {
				t.Fatal(err)
			}
			leader, follower := manager, named
			if reverse {
				leader, follower = named, manager
			}
			started := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			firstDone := make(chan issue148RememberResult, 1)
			go func() {
				value, err := leader.Remember("load", time.Minute, func() (interface{}, error) {
					close(started)
					<-release
					return "shared", nil
				})
				firstDone <- issue148RememberResult{value: value, err: err}
				close(firstDone)
			}()
			defer func() {
				unblock()
				awaitIssue148Remember(t, firstDone)
			}()
			awaitIssue148Signal(t, started, "首个 Remember 加载")
			// 添加字典序更小的别名，不得改变已开始加载的身份。
			if err := manager.RegisterStore("aaa", backend); err != nil {
				t.Fatal(err)
			}
			if !reverse {
				follower, err = manager.Store("aaa")
				if err != nil {
					t.Fatal(err)
				}
			}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &issue148WaitContext{Context: base, waiting: make(chan struct{})}
			duplicate := make(chan struct{})
			secondDone := make(chan issue148RememberResult, 1)
			go func() {
				value, err := follower.RememberContext(ctx, "load", time.Minute, func() (interface{}, error) {
					close(duplicate)
					return "duplicate", nil
				})
				secondDone <- issue148RememberResult{value: value, err: err}
				close(secondDone)
			}()
			defer func() {
				cancel()
				unblock()
				awaitIssue148Remember(t, secondDone)
			}()
			select {
			case <-ctx.waiting:
			case <-duplicate:
				t.Fatal("同一 driver 的别名执行了第二个 Remember 加载")
			case <-time.After(5 * time.Second):
				t.Fatal("具名别名未加入等待中的 Remember")
			}
			otherContext, cancelOther := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelOther()
			value, err := other.RememberContext(otherContext, "load", time.Minute, func() (interface{}, error) {
				return "independent", nil
			})
			if err != nil || value != "independent" {
				t.Fatalf("不同 store 不得加入同一个加载: value=%v err=%v", value, err)
			}
			unblock()
			for _, done := range []<-chan issue148RememberResult{firstDone, secondDone} {
				got := awaitIssue148Remember(t, done)
				if got.err != nil || got.value != "shared" {
					t.Fatalf("跨别名 Remember 结果错误: value=%v err=%v", got.value, got.err)
				}
			}
			assertIssue148Value(t, other, "load", "independent", true)
		})
	}
}

func awaitIssue148Remember(t *testing.T, done <-chan issue148RememberResult) issue148RememberResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("Remember 未完成")
		return issue148RememberResult{}
	}
}
