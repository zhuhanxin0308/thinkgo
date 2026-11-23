package context

import (
	stdcontext "context"
	"errors"
	"runtime"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

type requestResolverFunc func(string, ...interface{}) (interface{}, error)

func (resolve requestResolverFunc) Make(name string, params ...interface{}) (interface{}, error) {
	return resolve(name, params...)
}

type requestResolverWithResourceProbe struct {
	requestResolverFunc
	hasResources func() bool
}

func (resolver requestResolverWithResourceProbe) HasRequestScopedResources() bool {
	return resolver.hasResources()
}

// 回调内切换到目标应用后，嵌套解析使用新作用域，外层仍向原解析器查询资源状态。
func TestRequestResolverRebindsAndResolvesTarget(t *testing.T) {
	request := MustNewRequest(nil)
	var closeOrder []string
	origin := &requestServiceScopeProbe{name: "origin", order: &closeOrder}
	target := &requestServiceScopeProbe{name: "target", order: &closeOrder, values: map[string]interface{}{"service": "target value"}}
	final := &requestServiceScopeProbe{name: "final", order: &closeOrder, values: map[string]interface{}{"service": "final value"}}
	resolver := requestResolverWithResourceProbe{
		requestResolverFunc: func(name string, params ...interface{}) (interface{}, error) {
			// 此时没有其他协程操作请求。在锁回退时直接报告错误，避免测试卡在同步重绑定。
			if !request.serviceMu.TryLock() {
				return nil, errors.New("请求持有作用域锁进入了解析回调")
			}
			request.serviceMu.Unlock()
			if err := WithServiceScope(target, target)(request); err != nil {
				return nil, err
			}
			return request.Make(name, params...)
		},
		hasResources: func() bool {
			if !request.serviceMu.TryLock() {
				t.Fatal("请求持有作用域锁进入了资源探针")
			}
			request.serviceMu.Unlock()
			if err := WithServiceScope(final, final)(request); err != nil {
				t.Fatal(err)
			}
			return true
		},
	}
	if err := WithServiceScope(resolver, origin)(request); err != nil {
		t.Fatal(err)
	}
	if value, err := request.Make("service"); err != nil || value != "target value" {
		t.Fatalf("嵌套解析未使用目标作用域：value=%v err=%v", value, err)
	}
	if value, err := request.Make("service"); err != nil || value != "final value" {
		t.Fatalf("原解析器的资源探针未完成重绑定：value=%v err=%v", value, err)
	}
	if err := request.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(closeOrder, []string{"final", "target", "origin"}) {
		t.Fatalf("请求应按绑定逆序各关闭一次：%v", closeOrder)
	}
}

type requestIdleScopeProbe struct {
	requestServiceScopeProbe
}

func (scope *requestIdleScopeProbe) CloseIfIdle() (bool, error) {
	return true, scope.Close()
}

// 即使关闭器本身空闲，也必须等独立解析器的所有已准入回调离开才能关闭。
func TestRequestCleanupDrainsResolverOnEveryExit(t *testing.T) {
	panicValue := errors.New("解析中止")
	for _, test := range []struct {
		name      string
		exit      func()
		wantPanic interface{}
	}{
		{name: "正常返回", exit: func() {}},
		{name: "panic", exit: func() { panic(panicValue) }, wantPanic: panicValue},
		{name: "协程退出", exit: runtime.Goexit},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered := make(chan struct{})
				release := make(chan struct{})
				closer := &requestIdleScopeProbe{}
				resolver := requestResolverFunc(func(string, ...interface{}) (interface{}, error) {
					close(entered)
					<-release
					test.exit()
					return nil, nil
				})
				request := MustNewRequest(nil, WithTrustedIdleServiceScope(resolver, closer))
				exited := make(chan interface{}, 1)
				go func() {
					defer func() { exited <- recover() }()
					_, _ = request.Make("service")
				}()
				<-entered
				if !request.CleanupRequiresSupervisor() {
					t.Error("在途解析仍持有请求资源，HTTP 收尾应启用监督")
				}
				cleaned := make(chan error, 1)
				go func() { cleaned <- request.Cleanup() }()
				synctest.Wait()
				if calls := closer.closed.Load(); calls != 0 {
					t.Errorf("在途解析退出前关闭器已执行 %d 次", calls)
				}
				if _, err := request.Make("late"); !errors.Is(err, ErrRequestServiceScopeUnavailable) {
					t.Errorf("清理应拒绝新解析，实际为 %v", err)
				}
				close(release)
				if recovered := <-exited; recovered != test.wantPanic {
					t.Errorf("解析回调的异常处理发生变化：%v", recovered)
				}
				if err := <-cleaned; err != nil {
					t.Fatal(err)
				}
				if calls := closer.closed.Load(); calls != 1 {
					t.Fatalf("资源应在解析退出后关闭一次，实际为 %d", calls)
				}
			})
		})
	}
}

// 嵌套解析先返回时，外层回调仍有权使用已取得的服务。
func TestRequestCleanupWaitsForOutermostResolver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		innerEntered := make(chan struct{})
		innerRelease := make(chan struct{})
		outerRelease := make(chan struct{})
		innerReturned := make(chan struct{})
		request := MustNewRequest(nil)
		closer := &requestServiceScopeProbe{}
		resolver := requestResolverFunc(func(name string, _ ...interface{}) (interface{}, error) {
			if name == "inner" {
				close(innerEntered)
				<-innerRelease
				return "nested value", nil
			}
			value, err := request.Make("inner")
			close(innerReturned)
			<-outerRelease
			return value, err
		})
		if err := WithServiceScope(resolver, closer)(request); err != nil {
			t.Fatal(err)
		}
		returned := make(chan struct{})
		go func() {
			defer close(returned)
			if value, err := request.Make("outer"); err != nil || value != "nested value" {
				t.Errorf("已准入的嵌套解析结果错误：value=%v err=%v", value, err)
			}
		}()
		<-innerEntered
		cleaned := make(chan error, 1)
		go func() { cleaned <- request.Cleanup() }()
		synctest.Wait()
		close(innerRelease)
		<-innerReturned
		synctest.Wait()
		if calls := closer.closed.Load(); calls != 0 {
			t.Errorf("内层返回后，外层使用中的资源已被关闭 %d 次", calls)
		}
		close(outerRelease)
		<-returned
		if err := <-cleaned; err != nil {
			t.Fatal(err)
		}
		if calls := closer.closed.Load(); calls != 1 {
			t.Fatalf("全部回调退出后应关闭一次，实际为 %d", calls)
		}
	})
}

// 回调可以用有截止时间的清理封闭请求，超时只结束此次等待，实际关闭在回调退出后完成。
func TestRequestResolverCanStartCleanupWithDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		request := MustNewRequest(nil)
		closer := &requestServiceScopeProbe{}
		resolver := requestResolverFunc(func(string, ...interface{}) (interface{}, error) {
			ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), time.Second)
			defer cancel()
			err := request.CleanupContext(ctx)
			if calls := closer.closed.Load(); calls != 0 {
				t.Errorf("回调内的清理提前关闭了正在使用的资源：%d 次", calls)
			}
			if bindErr := WithServiceScope(closer, closer)(request); !errors.Is(bindErr, ErrRequestCleaned) {
				t.Errorf("清理等待返回前应封闭重绑定，实际为 %v", bindErr)
			}
			return nil, err
		})
		if err := WithServiceScope(resolver, closer)(request); err != nil {
			t.Fatal(err)
		}
		if _, err := request.Make("service"); !errors.Is(err, stdcontext.DeadlineExceeded) {
			t.Fatalf("回调内等待清理应按截止时间返回，实际为 %v", err)
		}
		if err := request.Cleanup(); err != nil {
			t.Fatalf("回调退出后完整清理失败：%v", err)
		}
		if calls := closer.closed.Load(); calls != 1 {
			t.Fatalf("回调退出后关闭器应只执行一次，实际为 %d", calls)
		}
	})
}
