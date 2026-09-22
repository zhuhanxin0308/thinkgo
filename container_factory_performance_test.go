package framework

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

var containerScopeAllocationSink *ContainerScope

// TestContainerEmptyScopeAllocationBudget 防止空作用域重新分配空索引表或拆分独占容器的存储。
func TestContainerEmptyScopeAllocationBudget(t *testing.T) {
	container := NewContainer()
	allocations := testing.AllocsPerRun(100, func() {
		scope := container.NewScope()
		if closed, err := scope.CloseIfIdle(); !closed || err != nil {
			panic("空作用域必须同步关闭")
		}
		containerScopeAllocationSink = scope
	})
	containerScopeAllocationSink = nil
	const emptyScopeAllocationBudget = 1
	if allocations > emptyScopeAllocationBudget {
		t.Fatalf("空作用域分配超过预算: got=%g maximum=%d", allocations, emptyScopeAllocationBudget)
	}
}

// TestContainerRequestFactoryArguments 保证同签名自定义工厂保持参数赋值、空值及可变参数语义。
func TestContainerRequestFactoryArguments(t *testing.T) {
	container := NewContainer()
	request := &http.Request{}
	result := new(fwcontext.Request)
	var received *http.Request
	var options []fwcontext.RequestOption
	container.BindFactory("custom.request", func(raw *http.Request, values ...fwcontext.RequestOption) (*fwcontext.Request, error) {
		received, options = raw, values
		return result, nil
	})
	for _, raw := range []interface{}{request, (*http.Request)(nil), nil} {
		value, err := container.Make("custom.request", raw)
		if err != nil || value != result || options == nil || len(options) != 0 {
			t.Fatalf("无可变参数调用语义变化: value=%v options=%v err=%v", value, options, err)
		}
		if raw == request && received != request {
			t.Fatal("原始请求指针未保持")
		}
		if raw != request && received != nil {
			t.Fatal("空请求参数没有按指针零值传递")
		}
	}
	// 未命名函数可以赋值给 RequestOption，不能仅用类型断言错误地拒绝。
	unnamedOption := func(*fwcontext.Request) error { return nil }
	if _, err := container.Make("custom.request", request, unnamedOption, nil); err != nil {
		t.Fatalf("合法的可赋值选项被拒绝: %v", err)
	}
	if len(options) != 2 || options[0] == nil || options[1] != nil {
		t.Fatalf("可变参数顺序或空值变化: %v", options)
	}
	for _, parameters := range [][]interface{}{nil, {"bad"}, {request, "bad"}} {
		if _, err := container.Make("custom.request", parameters...); err == nil {
			t.Fatalf("非法参数必须被拒绝: %v", parameters)
		}
	}
}

// TestContainerRequestFactoryErrors 验证直接调用候选签名仍将显式错误和 panic 转换为解析失败。
func TestContainerRequestFactoryErrors(t *testing.T) {
	want := errors.New("自定义请求工厂失败")
	var nilFactory func(*http.Request, ...fwcontext.RequestOption) (*fwcontext.Request, error)
	cases := []struct {
		name    string
		factory interface{}
		match   string
	}{
		{name: "returned", factory: func(*http.Request, ...fwcontext.RequestOption) (*fwcontext.Request, error) { return nil, want }, match: want.Error()},
		{name: "panic", factory: func(*http.Request, ...fwcontext.RequestOption) (*fwcontext.Request, error) {
			panic("请求构造 panic")
		}, match: "工厂执行 panic"},
		{name: "nil", factory: nilFactory, match: "工厂执行 panic"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			container := NewContainer()
			container.BindFactory("request", item.factory)
			if _, err := container.Make("request", nil); err == nil || !strings.Contains(err.Error(), item.match) {
				t.Fatalf("工厂错误未保持: %v", err)
			}
		})
	}
}

// TestContainerFactoryContextAndReplacement 验证调用计划不会截断上下文或在重绑定后继续调用旧工厂。
func TestContainerFactoryContextAndReplacement(t *testing.T) {
	container := NewContainer()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	container.BindFactory("contextual", func(current *Container) (interface{}, error) {
		return nil, current.Context().Err()
	})
	if _, err := container.MakeContext(ctx, "contextual"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消上下文未传入工厂: %v", err)
	}
	var builds atomic.Int32
	container.BindFactory("value", func() interface{} { return builds.Add(1) })
	if _, err := container.Make("value"); err != nil {
		t.Fatal(err)
	}
	container.BindFactory("value", func() (interface{}, error) { return "replacement", nil })
	value, err := container.Make("value")
	if err != nil || value != "replacement" || builds.Load() != 1 {
		t.Fatalf("重绑定没有更换工厂: value=%v builds=%d err=%v", value, builds.Load(), err)
	}
	container.BindFactory("loop", func(current *Container) interface{} { return current.Get("loop") })
	if _, err := container.Make("loop"); err == nil || !strings.Contains(err.Error(), "circular") {
		t.Fatalf("直接调用仍须检测循环依赖: %v", err)
	}
}

// TestContainerReflectedFactoryArgumentContract 保留命名函数、接口参数及返回值的反射回退契约。
func TestContainerReflectedFactoryArgumentContract(t *testing.T) {
	type namedFactory func(*Container, string, ...int) (interface{}, error)
	container := NewContainer()
	container.Instance("prefix", "prefix")
	container.BindFactory("named", namedFactory(func(current *Container, value string, numbers ...int) (interface{}, error) {
		return []interface{}{current.Get("prefix"), value, numbers}, nil
	}))
	value, err := container.Make("named", "value", 1, 2)
	if err != nil || !reflect.DeepEqual(value, []interface{}{"prefix", "value", []int{1, 2}}) {
		t.Fatalf("命名工厂反射回退失败: value=%v err=%v", value, err)
	}
	for _, parameters := range [][]interface{}{nil, {nil}, {"value", "bad"}} {
		if _, err := container.Make("named", parameters...); err == nil {
			t.Fatalf("命名工厂非法参数必须被拒绝: %v", parameters)
		}
	}
}

// TestContainerRequestFactoryWithScopedContext 保持自定义请求 Provider 的容器注入和请求作用域依赖。
func TestContainerRequestFactoryWithScopedContext(t *testing.T) {
	container := NewContainer()
	dependency := new(fwcontext.Request)
	container.BindScoped("dependency", func() interface{} { return dependency })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	container.BindFactory("custom", func(current *Container, raw *http.Request, options ...fwcontext.RequestOption) (*fwcontext.Request, error) {
		if current.Context() != ctx || current.Context().Err() != context.Canceled {
			return nil, errors.New("请求上下文丢失")
		}
		if raw != nil || options == nil || len(options) != 0 {
			return nil, errors.New("请求参数改变")
		}
		value, err := current.Make("dependency")
		if err != nil {
			return nil, err
		}
		return value.(*fwcontext.Request), nil
	})
	scope := container.NewScope()
	defer func() { _ = scope.Close() }()
	value, err := scope.MakeContext(ctx, "custom", nil)
	if err != nil || value != dependency {
		t.Fatalf("注入容器没有保持请求上下文和作用域: value=%v err=%v", value, err)
	}
}

// TestContainerDirectFactoryRejectsStaleGeneration 验证三种生命周期的直接调用都拒绝重绑定前的在途结果。
func TestContainerDirectFactoryRejectsStaleGeneration(t *testing.T) {
	for _, lifecycle := range []LifecycleType{Factory, Singleton, Scoped} {
		t.Run(map[LifecycleType]string{Factory: "factory", Singleton: "singleton", Scoped: "scoped"}[lifecycle], func(t *testing.T) {
			container := NewContainer()
			scope := container.NewScope()
			defer func() { _ = scope.Close() }()
			started := make(chan struct{})
			release := make(chan struct{})
			bind := func(factory interface{}) {
				switch lifecycle {
				case Factory:
					container.BindFactory("service", factory)
				case Singleton:
					container.Bind("service", factory)
				case Scoped:
					container.BindScoped("service", factory)
				}
			}
			bind(func(*http.Request, ...fwcontext.RequestOption) (*fwcontext.Request, error) {
				close(started)
				<-release
				return new(fwcontext.Request), nil
			})
			finished := make(chan error, 1)
			go func() {
				_, err := scope.Make("service", nil)
				finished <- err
			}()
			<-started
			replacement := new(fwcontext.Request)
			bind(func(*http.Request, ...fwcontext.RequestOption) (*fwcontext.Request, error) { return replacement, nil })
			close(release)
			if err := <-finished; err == nil || !strings.Contains(err.Error(), "binding changed") {
				t.Fatalf("旧代际工厂结果未失效: %v", err)
			}
			if value, err := scope.Make("service", nil); err != nil || value != replacement {
				t.Fatalf("新代际工厂未生效: value=%v err=%v", value, err)
			}
		})
	}
}

// TestContainerLazyScopeWaitGraph 防止懒初始化遗漏并发 Scoped 构建的循环等待索引。
func TestContainerLazyScopeWaitGraph(t *testing.T) {
	container := NewContainer()
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	container.BindScoped("first", func(current *Container) (interface{}, error) {
		close(firstStarted)
		<-secondStarted
		return current.Make("second")
	})
	container.BindScoped("second", func(current *Container) (interface{}, error) {
		close(secondStarted)
		<-firstStarted
		return current.Make("first")
	})
	scope := container.NewScope()
	results := make(chan error, 2)
	for _, service := range []string{"first", "second"} {
		go func() {
			_, err := scope.Make(service)
			results <- err
		}()
	}
	const resolutionDeadline = 5 * time.Second
	timer := time.NewTimer(resolutionDeadline)
	defer timer.Stop()
	for range 2 {
		select {
		case err := <-results:
			if err == nil || !strings.Contains(err.Error(), "circular") {
				t.Fatalf("Scoped 循环应返回明确错误: %v", err)
			}
		case <-timer.C:
			t.Fatal("Scoped 循环构建发生死锁")
		}
	}
	if err := scope.Close(); err != nil {
		t.Fatalf("构建失败后的空作用域关闭失败: %v", err)
	}
}

// TestAutomaticModelFactoryAllocationMatchesRegisteredFactory 防止自动模型热路径重复分析已经装配的工厂签名。
func TestAutomaticModelFactoryAllocationMatchesRegisteredFactory(t *testing.T) {
	app := newAutomaticModelApp(t, "allocation")
	if err := app.BindFactory("manual.model", automaticModelFactory(reflect.TypeFor[automaticUserModel]())); err != nil {
		t.Fatalf("注册等价模型工厂失败: %v", err)
	}
	measure := func(key string) float64 {
		return testing.AllocsPerRun(100, func() {
			value, err := app.Make(key)
			if err != nil || value.(*automaticUserModel).Model == nil {
				panic("模型工厂必须构造完整的独立模型")
			}
		})
	}
	manual := measure("manual.model")
	typedKey, err := modelTypeServiceKey(reflect.TypeFor[*automaticUserModel]())
	if err != nil {
		t.Fatalf("获取模型类型键失败: %v", err)
	}
	for _, key := range []string{"model.User", typedKey} {
		automatic := measure(key)
		if automatic > manual {
			t.Fatalf("自动模型工厂 %s 比相同的显式工厂增加热路径分配: automatic=%g manual=%g", key, automatic, manual)
		}
	}
}

// BenchmarkContainerRequestFactoryDispatch 隔离容器调度，避免请求构造器的其它优化混入当前分配证据。
func BenchmarkContainerRequestFactoryDispatch(b *testing.B) {
	container := NewContainer()
	request := new(fwcontext.Request)
	container.BindFactory("request", func(*http.Request, ...fwcontext.RequestOption) (*fwcontext.Request, error) { return request, nil })
	parameters := []interface{}{new(http.Request), fwcontext.RequestOption(func(*fwcontext.Request) error { return nil })}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := container.MakeContext(ctx, "request", parameters...); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkContainerEmptyScope 记录没有 Scoped 资源的请求作用域创建和同步关闭成本。
func BenchmarkContainerEmptyScope(b *testing.B) {
	container := NewContainer()
	b.ReportAllocs()
	for b.Loop() {
		scope := container.NewScope()
		if closed, err := scope.CloseIfIdle(); !closed || err != nil {
			b.Fatalf("空作用域关闭失败: closed=%v err=%v", closed, err)
		}
	}
}
