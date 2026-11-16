package framework

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestContainerBuildExitSignalsWaitersAndAllowsRetry(t *testing.T) {
	for _, lifecycle := range []struct {
		name   string
		scoped bool
	}{{name: "单例"}, {name: "作用域", scoped: true}} {
		t.Run(lifecycle.name, func(t *testing.T) {
			const service = "abandoned-build"
			container := NewContainer()
			makeService := container.Make
			entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
			calls := 0
			factory := func() any {
				calls++
				if calls == 1 {
					close(entered)
					<-release
					runtime.Goexit()
				}
				return "ready"
			}
			var scope *ContainerScope
			if lifecycle.scoped {
				container.BindScoped(service, factory)
				scope = container.NewScope()
				makeService = scope.Make
			} else {
				container.Bind(service, factory)
			}
			go func() {
				defer close(exited)
				_, _ = makeService(service)
			}()
			<-entered
			var active *buildState
			if scope == nil {
				state := container.sharedState()
				state.lock.RLock()
				active = state.building[service]
				state.lock.RUnlock()
			} else {
				scope.state.lock.Lock()
				for _, building := range scope.state.building {
					active = building
				}
				scope.state.lock.Unlock()
			}
			close(release)
			<-exited
			if active == nil {
				t.Fatal("已进入工厂的调用应登记在途状态")
			}
			select {
			case <-active.done:
				if !errors.Is(active.err, ErrContainerBuildAborted) || active.instance != nil {
					t.Fatalf("未返回的工厂不能发布实例: %#v，%v", active.instance, active.err)
				}
			default:
				t.Fatal("工厂退出时没有释放其余等待者")
			}
			if instance, err := makeService(service); err != nil || instance != "ready" || calls != 2 {
				t.Fatalf("后续调用不能重试: %#v，%v，调用 %d 次", instance, err, calls)
			}
			if scope != nil {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				if err := scope.CloseContext(ctx); err != nil {
					t.Fatalf("作用域应在清理后关闭: %v", err)
				}
			}
		})
	}
}
