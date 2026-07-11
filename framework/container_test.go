package framework

import (
	"reflect"
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
