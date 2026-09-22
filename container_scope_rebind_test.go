package framework

import (
	"sync/atomic"
	"testing"
	"time"
)

type reboundScopedResource struct {
	closed atomic.Int32
}

func (resource *reboundScopedResource) Close() error {
	resource.closed.Add(1)
	return nil
}

// TestScopedInvalidatedBuildRemainsOwned 验证旧构建失效后仍由作用域负责释放资源。
func TestScopedInvalidatedBuildRemainsOwned(t *testing.T) {
	const waitBudget = 2 * time.Second
	for _, remove := range []bool{false, true} {
		name := "重绑定"
		if remove {
			name = "删除"
		}
		t.Run(name, func(t *testing.T) {
			container := NewContainer()
			oldResource, newResource := &reboundScopedResource{}, &reboundScopedResource{}
			entered, release := make(chan struct{}), make(chan struct{})
			container.BindScoped("resource", func() interface{} {
				close(entered)
				<-release
				return oldResource
			})
			scope := container.NewScope()
			finished := make(chan error, 1)
			go func() { _, err := scope.Make("resource"); finished <- err }()
			select {
			case <-entered:
			case <-time.After(waitBudget):
				close(release)
				t.Fatal("工厂未开始构建")
			}
			if remove {
				container.Delete("resource")
			} else {
				container.BindScoped("resource", func() interface{} { return newResource })
			}
			close(release)
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("旧构建未报告绑定失效")
				}
			case <-time.After(waitBudget):
				t.Fatal("旧构建未返回")
			}
			if !remove {
				value, err := scope.Make("resource")
				if err != nil || value != newResource || newResource.closed.Load() != 0 {
					t.Fatalf("新绑定受到旧构建影响: value=%v err=%v", value, err)
				}
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := scope.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if oldResource.closed.Load() != 1 || !remove && newResource.closed.Load() != 1 {
				t.Fatalf("作用域资源未恰好释放一次: old=%d new=%d", oldResource.closed.Load(), newResource.closed.Load())
			}
		})
	}
}
