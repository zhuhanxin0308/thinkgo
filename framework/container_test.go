package framework

import (
	"reflect"
	"sync"
	"testing"
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
