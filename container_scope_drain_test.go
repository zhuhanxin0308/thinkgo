package framework

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestContainerScopeDrainsAdmittedResolutions(t *testing.T) {
	for _, entry := range []string{"Make", "MakeContext", "保留的注入容器", "Singleton"} {
		t.Run(entry, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				container := NewContainer()
				scope := container.NewScope()
				resolve := scope.Make
				switch entry {
				case "MakeContext":
					resolve = func(name string, params ...any) (any, error) {
						return scope.MakeContext(t.Context(), name, params...)
					}
				case "保留的注入容器":
					container.BindFactory("resolver", func(current *Container) any { return current })
					resolve = scope.Get("resolver").(*Container).Make
				}
				entered, release := make(chan struct{}), make(chan struct{})
				product := &signalingScopedCloseProbe{closed: make(chan struct{})}
				bind := container.BindFactory
				if entry == "Singleton" {
					bind = container.Bind
				}
				bind("slow", func() any {
					close(entered)
					<-release
					return product
				})
				finished := make(chan error, 1)
				go func() {
					got, err := resolve("slow")
					if err == nil && got != product {
						err = errors.New("解析未返回工厂实例")
					}
					finished <- err
				}()
				<-entered
				if !scope.HasRequestScopedResources() {
					t.Error("在途解析需要由完整关闭流程等待")
				}
				if closed, err := scope.CloseIfIdle(); closed || err != nil {
					t.Errorf("在途解析误报空闲: closed=%v err=%v", closed, err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if err := scope.CloseContext(ctx); !errors.Is(err, context.Canceled) {
					t.Errorf("应允许调用方取消等待: %v", err)
				}
				if _, err := resolve("unknown"); !errors.Is(err, ErrContainerScopeClosed) {
					t.Errorf("关闭开始后仍准入新解析: %v", err)
				}
				closeDone := make(chan error, 1)
				go func() { closeDone <- scope.Close() }()
				synctest.Wait()
				if len(closeDone) != 0 {
					t.Error("工厂返回前作用域已报告关闭完成")
				}
				close(release)
				if err := <-finished; err != nil {
					t.Errorf("已准入的解析失败: %v", err)
				}
				if err := <-closeDone; err != nil || product.count.Load() != 0 {
					t.Errorf("关闭改变了产物所有权: err=%v closed=%d", err, product.count.Load())
				}
			})
		})
	}
}

func TestContainerFactoryCanBoundWaitForItsOwnScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		container := NewContainer()
		scope := container.NewScope()
		container.BindFactory("self", func() (any, error) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			return nil, scope.CloseContext(ctx)
		})
		if _, err := scope.Make("self"); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("工厂在自身作用域中应能以截止时间结束关闭等待: %v", err)
		}
		if err := scope.Close(); err != nil {
			t.Fatalf("工厂返回后仍未完成关闭: %v", err)
		}
	})
}

func TestContainerScopeClosesAfterAdmittedBindingLookup(t *testing.T) {
	container := NewContainer()
	resource := &signalingScopedCloseProbe{closed: make(chan struct{})}
	container.BindScoped("late", func() any { return resource })
	scope := container.NewScope()
	global := container.sharedState()
	global.lock.Lock()
	// 写锁使准入后的解析停在绑定查找之前，工厂只能在测试主动解锁后启动。
	result := make(chan error, 1)
	go func() {
		_, err := scope.Make("late")
		result <- err
	}()
	watchdog := time.NewTimer(time.Second)
	defer watchdog.Stop()
	for !scope.HasRequestScopedResources() {
		select {
		case <-watchdog.C:
			global.lock.Unlock()
			<-result
			t.Fatal("查找绑定前尚未登记解析")
		default:
			runtime.Gosched()
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := scope.CloseContext(ctx)
	global.lock.Unlock()
	if !errors.Is(err, context.Canceled) {
		t.Errorf("关闭没有等待已准入的解析: %v", err)
	}
	if err := <-result; err != nil {
		t.Errorf("关闭截断了已准入的 Scoped 构建: %v", err)
	}
	if err := scope.Close(); err != nil || resource.count.Load() != 1 {
		t.Errorf("遗漏准入后才创建的资源: err=%v closed=%d", err, resource.count.Load())
	}
}

func TestContainerScopePreservesScopedDependencyUntilFactoryExits(t *testing.T) {
	factoryErr := errors.New("工厂失败")
	for _, exit := range []string{"error", "panic", "Goexit"} {
		t.Run(exit, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				container := NewContainer()
				dependency := &signalingScopedCloseProbe{closed: make(chan struct{})}
				container.BindScoped("dependency", func() any { return dependency })
				entered, release := make(chan struct{}), make(chan struct{})
				container.BindFactory("worker", func(current *Container) (any, error) {
					if _, err := current.Make("dependency"); err != nil {
						return nil, err
					}
					close(entered)
					<-release
					switch exit {
					case "panic":
						panic("工厂异常")
					case "Goexit":
						runtime.Goexit()
					}
					return nil, factoryErr
				})
				scope := container.NewScope()
				var makeErr error
				exited := make(chan struct{})
				go func() {
					defer close(exited)
					_, makeErr = scope.Make("worker")
				}()
				<-entered
				closed := make(chan error, 1)
				go func() { closed <- scope.Close() }()
				synctest.Wait()
				if dependency.count.Load() != 0 {
					t.Error("工厂仍在使用的 Scoped 依赖被提前关闭")
				}
				close(release)
				<-exited
				if exit == "error" && !errors.Is(makeErr, factoryErr) ||
					exit == "panic" && (makeErr == nil || !strings.Contains(makeErr.Error(), "工厂异常")) {
					t.Errorf("工厂退出原因被覆盖: %v", makeErr)
				}
				if err := <-closed; err != nil || dependency.count.Load() != 1 {
					t.Errorf("工厂退出后应关闭依赖一次: err=%v closed=%d", err, dependency.count.Load())
				}
			})
		})
	}
}
