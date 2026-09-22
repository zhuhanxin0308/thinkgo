package framework

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

// TestContainerContextFactoryAllocationBudget 防止首层解析状态与唯一栈帧重新拆为两次分配。
func TestContainerContextFactoryAllocationBudget(t *testing.T) {
	container := NewContainer()
	request := new(fwcontext.Request)
	container.BindFactory("request", func(*http.Request, ...fwcontext.RequestOption) (*fwcontext.Request, error) {
		return request, nil
	})
	parameters := []interface{}{new(http.Request), fwcontext.RequestOption(func(*fwcontext.Request) error { return nil })}
	ctx := context.Background()
	allocations := testing.AllocsPerRun(100, func() {
		value, err := container.MakeContext(ctx, "request", parameters...)
		if err != nil || value != request {
			panic("上下文工厂必须返回绑定结果")
		}
	})
	const contextFactoryAllocationBudget = 2
	if allocations > contextFactoryAllocationBudget {
		t.Fatalf("上下文工厂分配超过预算: got=%g maximum=%d", allocations, contextFactoryAllocationBudget)
	}
}

// TestContainerRetainedResolutionBranchesAreIsolated 验证保留的工厂容器可并发派生解析链，互不覆盖上下文或栈帧。
func TestContainerRetainedResolutionBranchesAreIsolated(t *testing.T) {
	type contextKey struct{}
	container := NewContainer()
	container.BindFactory("parent", func(current *Container) interface{} { return current })
	container.BindFactory("child", func(current *Container) interface{} { return current })
	container.BindFactory("leaf", func(current *Container) interface{} { return current.Context().Value(contextKey{}) })
	scope := container.NewScope()
	t.Cleanup(func() {
		if err := scope.Close(); err != nil {
			t.Errorf("关闭测试作用域失败: %v", err)
		}
	})
	parentContext := context.WithValue(context.Background(), contextKey{}, "parent")
	value, err := scope.MakeContext(parentContext, "parent")
	if err != nil {
		t.Fatalf("创建保留容器失败: %v", err)
	}
	parent := value.(*Container)
	const concurrentBranches = 8
	results := make(chan error, concurrentBranches)
	for branch := range concurrentBranches {
		go func() {
			childContext := context.WithValue(parentContext, contextKey{}, branch)
			childValue, childErr := parent.MakeContext(childContext, "child")
			if childErr != nil {
				results <- childErr
				return
			}
			child := childValue.(*Container)
			leaf, leafErr := child.Make("leaf")
			if leafErr != nil || leaf != branch || child.Context() != childContext {
				results <- fmt.Errorf("分支上下文被覆盖: branch=%d leaf=%v err=%v", branch, leaf, leafErr)
				return
			}
			if _, cycleErr := child.Make("parent"); cycleErr == nil || !strings.Contains(cycleErr.Error(), "parent -> child -> parent") {
				results <- fmt.Errorf("分支循环依赖链丢失: %v", cycleErr)
				return
			}
			results <- nil
		}()
	}
	for range concurrentBranches {
		if branchErr := <-results; branchErr != nil {
			t.Error(branchErr)
		}
	}
	leaf, err := parent.Make("leaf")
	if err != nil || leaf != "parent" || parent.Context() != parentContext {
		t.Fatalf("子解析污染父容器: leaf=%v err=%v", leaf, err)
	}
}
