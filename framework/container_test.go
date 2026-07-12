package framework

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestContainerBindAndMake 验证基本绑定和解析
func TestContainerBindAndMake(t *testing.T) {
	c := NewContainer()
	c.Bind("service", "hello")

	val, err := c.Make("service")
	if err != nil {
		t.Fatal(err)
	}
	if val != "hello" {
		t.Fatalf("期望 hello，实际 %v", val)
	}
}

// TestContainerInstance 验证 Instance 绑定
func TestContainerInstance(t *testing.T) {
	c := NewContainer()
	c.Instance("db", "mysql_connection")

	val, err := c.Make("db")
	if err != nil {
		t.Fatal(err)
	}
	if val != "mysql_connection" {
		t.Fatalf("期望 mysql_connection，实际 %v", val)
	}
}

// TestContainerSingleton 验证 Singleton 行为（同一实例）
func TestContainerSingleton(t *testing.T) {
	c := NewContainer()

	type Service struct{ ID int }
	counter := 0
	c.Bind("counter_service", func() interface{} {
		counter++
		return &Service{ID: counter}
	})

	val1, _ := c.Make("counter_service")
	val2, _ := c.Make("counter_service")

	// Singleton 模式：第二次应返回缓存实例
	if val1.(*Service).ID != val2.(*Service).ID {
		t.Fatal("Singleton 模式下应返回同一实例")
	}
	if counter != 1 {
		t.Fatalf("Singleton 模式下工厂函数只应调用一次，实际 %d 次", counter)
	}
}

// TestContainerFactory 验证 Factory 行为（每次新实例）
func TestContainerFactory(t *testing.T) {
	c := NewContainer()

	type Service struct{ ID int }
	counter := 0
	c.BindFactory("request_service", func() interface{} {
		counter++
		return &Service{ID: counter}
	})

	val1, _ := c.Make("request_service")
	val2, _ := c.Make("request_service")

	// Factory 模式：每次应创建新实例
	if val1.(*Service).ID == val2.(*Service).ID {
		t.Fatal("Factory 模式下应每次创建新实例")
	}
	if counter != 2 {
		t.Fatalf("Factory 模式下工厂函数应调用 2 次，实际 %d 次", counter)
	}
}

// TestContainerReflectType 验证 reflect.Type 自动实例化
func TestContainerReflectType(t *testing.T) {
	c := NewContainer()

	type MyController struct {
		Name string
	}
	c.BindFactory("controller", reflect.TypeOf(MyController{}))

	val1, err := c.Make("controller")
	if err != nil {
		t.Fatal(err)
	}
	val2, err := c.Make("controller")
	if err != nil {
		t.Fatal(err)
	}

	// 应该是不同的实例
	ctrl1, ok1 := val1.(*MyController)
	ctrl2, ok2 := val2.(*MyController)
	if !ok1 || !ok2 {
		t.Fatal("应返回 *MyController 类型")
	}
	if ctrl1 == ctrl2 {
		t.Fatal("Factory + reflect.Type 应返回不同实例")
	}
}

// TestContainerHas 验证 Has 方法
func TestContainerHas(t *testing.T) {
	c := NewContainer()
	c.Bind("exists", "value")

	if !c.Has("exists") {
		t.Fatal("已绑定的服务 Has() 应返回 true")
	}
	if c.Has("not_exists") {
		t.Fatal("未绑定的服务 Has() 应返回 false")
	}
}

// TestContainerDelete 验证 Delete 方法
func TestContainerDelete(t *testing.T) {
	c := NewContainer()
	c.Bind("temp", "value")
	c.Delete("temp")

	if c.Has("temp") {
		t.Fatal("删除后 Has() 应返回 false")
	}
}

// TestContainerNotFound 验证未绑定服务的错误
func TestContainerNotFound(t *testing.T) {
	c := NewContainer()
	_, err := c.Make("nonexistent")
	if err == nil {
		t.Fatal("未绑定的服务应返回 error")
	}
}

// TestContainerConcurrentSafety 验证并发安全性
func TestContainerConcurrentSafety(t *testing.T) {
	c := NewContainer()
	c.Bind("shared", "value")

	var wg sync.WaitGroup
	errors := make(chan string, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			val, err := c.Make("shared")
			if err != nil {
				errors <- err.Error()
				return
			}
			if val != "value" {
				errors <- "值不正确"
			}
		}()
	}
	wg.Wait()
	close(errors)

	for err := range errors {
		t.Fatal(err)
	}
}

// TestContainerSingletonFactoryCanResolveDependency 验证单例工厂执行期间仍可解析依赖，
// 避免容器持有全局写锁调用工厂导致同协程递归解析时死锁。
func TestContainerSingletonFactoryCanResolveDependency(t *testing.T) {
	c := NewContainer()
	c.Bind("dependency", "ready")
	c.Bind("service", func(container *Container) (interface{}, error) {
		value, err := container.Make("dependency")
		if err != nil {
			return nil, err
		}
		return "service:" + value.(string), nil
	})

	done := make(chan struct{})
	var (
		value interface{}
		err   error
	)
	go func() {
		defer close(done)
		value, err = c.Make("service")
	}()

	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("单例工厂内解析依赖不应发生死锁")
	}
	if err != nil {
		t.Fatalf("解析服务失败: %v", err)
	}
	if value != "service:ready" {
		t.Fatalf("依赖解析结果错误，实际为 %v", value)
	}
}

// TestContainerDetectsCircularSingletonDependency 验证同一构建链内的单例循环依赖会返回错误而不是死锁。
func TestContainerDetectsCircularSingletonDependency(t *testing.T) {
	c := NewContainer()
	c.Bind("service", func(container *Container) (interface{}, error) {
		return container.Make("service")
	})

	done := make(chan error, 1)
	go func() {
		_, err := c.Make("service")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("循环依赖应返回错误")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("循环依赖不应导致容器死锁")
	}
}

// TestContainerConcurrentSingletonFactoryBuildsOnce 验证并发解析同名单例时只构建一次。
func TestContainerConcurrentSingletonFactoryBuildsOnce(t *testing.T) {
	c := NewContainer()

	type service struct{ id int }
	counter := 0
	var counterLock sync.Mutex
	c.Bind("service", func() interface{} {
		counterLock.Lock()
		defer counterLock.Unlock()
		counter++
		return &service{id: counter}
	})

	var wg sync.WaitGroup
	results := make(chan *service, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := c.Make("service")
			if err != nil {
				t.Errorf("并发解析服务失败: %v", err)
				return
			}
			results <- value.(*service)
		}()
	}
	wg.Wait()
	close(results)

	var first *service
	for item := range results {
		if first == nil {
			first = item
			continue
		}
		if item != first {
			t.Fatal("并发解析同名单例应返回同一个实例")
		}
	}
	if counter != 1 {
		t.Fatalf("同名单例工厂应只执行一次，实际执行 %d 次", counter)
	}
}

// TestContainerDetectsCrossGoroutineCircularDependency 验证两个并发构建链互相等待时会返回错误而非永久阻塞。
func TestContainerDetectsCrossGoroutineCircularDependency(t *testing.T) {
	c := NewContainer()
	aStarted := make(chan struct{})
	bStarted := make(chan struct{})
	c.Bind("a", func(container *Container) (interface{}, error) {
		close(aStarted)
		<-bStarted
		return container.Make("b")
	})
	c.Bind("b", func(container *Container) (interface{}, error) {
		close(bStarted)
		<-aStarted
		return container.Make("a")
	})

	results := make(chan error, 2)
	go func() {
		_, err := c.Make("a")
		results <- err
	}()
	go func() {
		_, err := c.Make("b")
		results <- err
	}()

	for index := 0; index < 2; index++ {
		select {
		case err := <-results:
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "circular") {
				t.Fatalf("跨协程循环依赖必须返回明确错误，实际为 %v", err)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("跨协程循环依赖不应造成死锁")
		}
	}
}

// TestContainerRebindInvalidatesCachedSingleton 验证重新绑定会立即丢弃旧单例。
func TestContainerRebindInvalidatesCachedSingleton(t *testing.T) {
	c := NewContainer()
	c.Bind("service", "old")
	if value, err := c.Make("service"); err != nil || value != "old" {
		t.Fatalf("首次解析失败，value=%v err=%v", value, err)
	}

	c.Bind("service", "new")
	value, err := c.Make("service")
	if err != nil || value != "new" {
		t.Fatalf("重新绑定后必须返回新实例，value=%v err=%v", value, err)
	}
}

// TestContainerRebindRejectsInFlightResult 验证重绑定期间完成的旧构建不会覆盖新绑定。
func TestContainerRebindRejectsInFlightResult(t *testing.T) {
	c := NewContainer()
	started := make(chan struct{})
	release := make(chan struct{})
	c.Bind("service", func() interface{} {
		close(started)
		<-release
		return "stale"
	})

	oldResult := make(chan error, 1)
	go func() {
		_, err := c.Make("service")
		oldResult <- err
	}()
	<-started
	c.Bind("service", "fresh")
	close(release)

	if err := <-oldResult; err == nil {
		t.Fatal("绑定已变化时，旧构建调用必须返回失效错误")
	}
	value, err := c.Make("service")
	if err != nil || value != "fresh" {
		t.Fatalf("旧构建不得覆盖新绑定，value=%v err=%v", value, err)
	}
}

// TestContainerDeletePreventsInFlightResurrection 验证删除服务后旧构建结果不能重新写回实例表。
func TestContainerDeletePreventsInFlightResurrection(t *testing.T) {
	c := NewContainer()
	started := make(chan struct{})
	release := make(chan struct{})
	c.Bind("service", func() interface{} {
		close(started)
		<-release
		return "stale"
	})

	oldResult := make(chan error, 1)
	go func() {
		_, err := c.Make("service")
		oldResult <- err
	}()
	<-started
	c.Delete("service")
	close(release)

	if err := <-oldResult; err == nil {
		t.Fatal("服务已删除时，旧构建调用必须返回失效错误")
	}
	if c.Has("service") {
		t.Fatal("旧构建完成后不得复活已删除服务")
	}
	if _, err := c.Make("service"); err == nil {
		t.Fatal("删除后的服务必须保持不可解析")
	}
}

// TestContainerFactoryFailuresReturnErrors 验证无效参数、无返回值和工厂 panic 均转换为 Make 错误。
func TestContainerFactoryFailuresReturnErrors(t *testing.T) {
	tests := []struct {
		name    string
		factory interface{}
		params  []interface{}
	}{
		{name: "missing_argument", factory: func(value string) interface{} { return value }},
		{name: "wrong_argument_type", factory: func(value int) interface{} { return value }, params: []interface{}{"bad"}},
		{name: "no_result", factory: func() {}},
		{name: "panic", factory: func() interface{} { panic("factory exploded") }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := NewContainer()
			c.BindFactory("service", test.factory)
			if _, err := c.Make("service", test.params...); err == nil {
				t.Fatal("无效工厂必须返回错误")
			}
		})
	}
}

// TestContainerInstanceOverridesInFlightFactory 验证显式实例优先于仍在执行的旧工厂。
func TestContainerInstanceOverridesInFlightFactory(t *testing.T) {
	c := NewContainer()
	started := make(chan struct{})
	release := make(chan struct{})
	c.Bind("service", func() interface{} {
		close(started)
		<-release
		return "stale"
	})

	done := make(chan error, 1)
	go func() {
		_, err := c.Make("service")
		done <- err
	}()
	<-started
	c.Instance("service", "explicit")
	close(release)
	if err := <-done; err == nil {
		t.Fatal("显式实例覆盖后旧构建必须返回失效错误")
	}

	value, err := c.Make("service")
	if err != nil || value != "explicit" {
		t.Fatalf("显式实例必须覆盖旧工厂，value=%v err=%v", value, err)
	}
}

// TestContainerFactorySupportsValidatedArguments 验证可变参数和可空参数在类型校验后正常传入工厂。
func TestContainerFactorySupportsValidatedArguments(t *testing.T) {
	c := NewContainer()
	c.BindFactory("variadic", func(prefix string, values ...int) interface{} {
		total := 0
		for _, value := range values {
			total += value
		}
		return prefix + ":" + fmt.Sprint(total)
	})
	c.BindFactory("nullable", func(values map[string]int) interface{} {
		return values == nil
	})

	value, err := c.Make("variadic", "sum", 1, 2, 3)
	if err != nil || value != "sum:6" {
		t.Fatalf("可变参数工厂解析失败，value=%v err=%v", value, err)
	}
	nullable, err := c.Make("nullable", nil)
	if err != nil || nullable != true {
		t.Fatalf("可空参数工厂解析失败，value=%v err=%v", nullable, err)
	}
}

// TestContainerFactoryReturnContract 验证工厂显式错误和非法返回签名都由 Make 返回。
func TestContainerFactoryReturnContract(t *testing.T) {
	expected := errors.New("dependency unavailable")
	tests := []struct {
		name    string
		factory interface{}
		match   string
	}{
		{name: "returned_error", factory: func() (interface{}, error) { return nil, expected }, match: expected.Error()},
		{name: "second_value_not_error", factory: func() (interface{}, string) { return nil, "bad" }, match: "error"},
		{name: "too_many_results", factory: func() (int, int, int) { return 1, 2, 3 }, match: "1 或 2"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := NewContainer()
			c.Bind("service", test.factory)
			_, err := c.Make("service")
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("工厂错误契约不符合预期，err=%v", err)
			}
		})
	}
}

// TestZeroValueContainerIsUsable 验证 Container 零值可以安全绑定和解析。
func TestZeroValueContainerIsUsable(t *testing.T) {
	var c Container
	c.Bind("service", "ready")
	value, err := c.Make("service")
	if err != nil || value != "ready" {
		t.Fatalf("Container 零值不可用，value=%v err=%v", value, err)
	}
}

// TestContainerGetContract 验证 Get 在成功时返回实例，失败时按契约 panic。
func TestContainerGetContract(t *testing.T) {
	c := NewContainer()
	c.Bind("service", "ready")
	if value := c.Get("service"); value != "ready" {
		t.Fatalf("Get 返回值错误，实际为 %v", value)
	}

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("Get 解析缺失服务时必须 panic")
		}
	}()
	c.Get("missing")
}

// TestContainerWaitGraphSeparatesBindingGenerations 验证旧代等待边不会把互不依赖的新代构建误判为循环。
func TestContainerWaitGraphSeparatesBindingGenerations(t *testing.T) {
	c := NewContainer()
	b1Started := make(chan struct{})
	releaseB1 := make(chan struct{})
	c.Bind("b", func() interface{} {
		close(b1Started)
		<-releaseB1
		return "old-b"
	})
	b1Done := make(chan error, 1)
	go func() {
		_, err := c.Make("b")
		b1Done <- err
	}()
	<-b1Started

	a1Resolving := make(chan struct{})
	c.Bind("a", func(container *Container) (interface{}, error) {
		close(a1Resolving)
		return container.Make("b")
	})
	a1Done := make(chan error, 1)
	go func() {
		_, err := c.Make("a")
		a1Done <- err
	}()
	<-a1Resolving
	// 等待旧代 A 建立到旧代 B 的等待边，避免依赖固定睡眠时间。
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		state := c.sharedState()
		state.lock.RLock()
		hasWait := len(state.waiting) > 0
		state.lock.RUnlock()
		if hasWait {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("旧代 A 未建立等待边")
		}
		runtime.Gosched()
	}

	a2Started := make(chan struct{})
	releaseA2 := make(chan struct{})
	c.Bind("a", func() interface{} {
		close(a2Started)
		<-releaseA2
		return "new-a"
	})
	a2Done := make(chan error, 1)
	go func() {
		_, err := c.Make("a")
		a2Done <- err
	}()
	<-a2Started

	c.Bind("b", func(container *Container) (interface{}, error) {
		return container.Make("a")
	})
	b2Result := make(chan struct {
		value interface{}
		err   error
	}, 1)
	go func() {
		value, err := c.Make("b")
		b2Result <- struct {
			value interface{}
			err   error
		}{value: value, err: err}
	}()

	select {
	case result := <-b2Result:
		t.Fatalf("新代 B 应等待新代 A，而不是被旧代等待边误判，value=%v err=%v", result.value, result.err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseA2)
	if err := <-a2Done; err != nil {
		t.Fatalf("新代 A 构建失败: %v", err)
	}
	result := <-b2Result
	if result.err != nil || result.value != "new-a" {
		t.Fatalf("新代 B 应解析新代 A，value=%v err=%v", result.value, result.err)
	}

	close(releaseB1)
	if err := <-b1Done; err == nil {
		t.Fatal("旧代 B 在重绑定后必须失效")
	}
	if err := <-a1Done; err == nil {
		t.Fatal("依赖旧代 B 的旧代 A 必须失效")
	}
}
