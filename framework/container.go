package framework

import (
	"fmt"
	"reflect"
	"sync"
)

// LifecycleType 服务生命周期类型
type LifecycleType int

const (
	// Singleton 单例模式（默认）：全生命周期只创建一个实例
	Singleton LifecycleType = iota
	// Factory 工厂模式：每次 Make() 都创建新实例
	Factory
)

// binding 服务绑定信息
type binding struct {
	concrete  interface{}   // 绑定的具体实现（实例、工厂函数或 reflect.Type）
	lifecycle LifecycleType // 生命周期类型
}

// Container 依赖注入容器
// 对应 ThinkPHP 8 的 think\Container
// 支持 Singleton 和 Factory 两种生命周期
type Container struct {
	bindings  map[string]binding     // 服务绑定注册表
	instances map[string]interface{} // 单例实例缓存
	lock      sync.RWMutex           // 读写锁（保护并发访问）
}

// NewContainer 创建容器实例
func NewContainer() *Container {
	return &Container{
		bindings:  make(map[string]binding),
		instances: make(map[string]interface{}),
	}
}

// Bind 绑定服务到容器（默认 Singleton 模式）
// abstract: 服务名称
// concrete: 实例、闭包工厂函数或 reflect.Type
func (c *Container) Bind(abstract string, concrete interface{}) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.bindings[abstract] = binding{concrete: concrete, lifecycle: Singleton}
}

// BindFactory 绑定工厂模式服务（每次 Make() 创建新实例）
// 适用于：控制器、请求级组件等需要每次创建新实例的场景
func (c *Container) BindFactory(abstract string, concrete interface{}) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.bindings[abstract] = binding{concrete: concrete, lifecycle: Factory}
}

// Instance 绑定已存在的实例（强制 Singleton）
func (c *Container) Instance(abstract string, instance interface{}) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.instances[abstract] = instance
}

// Make 从容器解析服务
// Singleton 模式：首次创建后缓存，后续返回缓存实例
// Factory 模式：每次创建新实例
func (c *Container) Make(abstract string, params ...interface{}) (interface{}, error) {
	c.lock.RLock()
	// 检查单例缓存
	if instance, ok := c.instances[abstract]; ok {
		c.lock.RUnlock()
		return instance, nil
	}
	b, ok := c.bindings[abstract]
	c.lock.RUnlock()

	if !ok {
		return nil, fmt.Errorf("binding not found: %s", abstract)
	}

	instance, err := c.build(b.concrete, params...)
	if err != nil {
		return nil, err
	}

	// Singleton 模式：缓存实例
	if b.lifecycle == Singleton {
		c.lock.Lock()
		c.instances[abstract] = instance
		c.lock.Unlock()
	}

	return instance, nil
}

// Get Make 的快捷版本（解析失败时 panic）
// 对应 ThinkPHP 的 app() 辅助函数
func (c *Container) Get(abstract string) interface{} {
	instance, err := c.Make(abstract)
	if err != nil {
		panic(err)
	}
	return instance
}

// Has 检查服务是否已绑定
func (c *Container) Has(abstract string) bool {
	c.lock.RLock()
	defer c.lock.RUnlock()
	_, okInstance := c.instances[abstract]
	_, okBinding := c.bindings[abstract]
	return okInstance || okBinding
}

// Delete 从容器中移除服务绑定和实例
func (c *Container) Delete(abstract string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	delete(c.bindings, abstract)
	delete(c.instances, abstract)
}

// build 构建服务实例
func (c *Container) build(concrete interface{}, params ...interface{}) (interface{}, error) {
	val := reflect.ValueOf(concrete)

	// 如果是函数（工厂闭包），调用并返回结果
	if val.Kind() == reflect.Func {
		in := make([]reflect.Value, 0, len(params)+1)

		// 检查函数是否接受 *Container 作为第一个参数
		if val.Type().NumIn() >= 1 && val.Type().In(0) == reflect.TypeOf(c) {
			in = append(in, reflect.ValueOf(c))
		}

		for _, param := range params {
			in = append(in, reflect.ValueOf(param))
		}

		res := val.Call(in)
		if len(res) == 0 {
			return nil, nil
		}

		// 处理 (value, error) 双返回值
		if len(res) == 2 && res[1].Type().Implements(reflect.TypeOf((*error)(nil)).Elem()) {
			if !res[1].IsNil() {
				return nil, res[1].Interface().(error)
			}
			return res[0].Interface(), nil
		}
		return res[0].Interface(), nil
	}

	// 如果是 reflect.Type，创建新实例
	if t, ok := concrete.(reflect.Type); ok {
		return reflect.New(t).Interface(), nil
	}

	// 其他情况直接返回（作为值绑定）
	return concrete, nil
}
