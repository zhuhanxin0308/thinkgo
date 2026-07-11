package framework

import (
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"strings"
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

// buildState 记录单例正在构建的状态，用于并发等待同一个构建结果。
type buildState struct {
	done     chan struct{}
	instance interface{}
	err      error
	owner    uint64
}

// Container 依赖注入容器
// 对应 ThinkPHP 8 的 think\Container
// 支持 Singleton 和 Factory 两种生命周期
type Container struct {
	bindings  map[string]binding     // 服务绑定注册表
	instances map[string]interface{} // 单例实例缓存
	building  map[string]*buildState // 正在构建的单例，避免并发重复构建
	lock      sync.RWMutex           // 读写锁（保护并发访问）
}

// NewContainer 创建容器实例
func NewContainer() *Container {
	return &Container{
		bindings:  make(map[string]binding),
		instances: make(map[string]interface{}),
		building:  make(map[string]*buildState),
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
//
// 单例创建使用双重检查锁，确保并发 Make 同一未缓存单例时只构建并缓存一个实例，
// 避免“重复构建 + 后写覆盖先写”导致不同 goroutine 拿到不同单例。
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

	// Factory 模式：每次都构建新实例，无需加锁缓存。
	if b.lifecycle != Singleton {
		return c.build(b.concrete, params...)
	}

	// Singleton 模式：登记构建状态后在锁外执行工厂。
	// 这样既能让并发调用共享同一次构建结果，也允许工厂内部继续解析其它依赖。
	c.lock.Lock()
	if instance, ok := c.instances[abstract]; ok {
		c.lock.Unlock()
		return instance, nil
	}
	if state, ok := c.building[abstract]; ok {
		if state.owner != 0 && state.owner == currentGoroutineID() {
			c.lock.Unlock()
			return nil, fmt.Errorf("circular singleton dependency detected: %s", abstract)
		}
		c.lock.Unlock()
		<-state.done
		return state.instance, state.err
	}
	state := &buildState{
		done:  make(chan struct{}),
		owner: currentGoroutineID(),
	}
	c.building[abstract] = state
	c.lock.Unlock()

	instance, err := c.buildSingleton(abstract, b.concrete, state, params...)
	if err != nil {
		return nil, err
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

// buildSingleton 在锁外构建单例，并在结束时唤醒所有等待相同服务的协程。
func (c *Container) buildSingleton(abstract string, concrete interface{}, state *buildState, params ...interface{}) (instance interface{}, err error) {
	defer func() {
		recovered := recover()
		if recovered != nil {
			err = fmt.Errorf("build singleton %s panic: %v", abstract, recovered)
		}

		c.lock.Lock()
		state.err = err
		if err == nil {
			c.instances[abstract] = instance
			state.instance = instance
		}
		delete(c.building, abstract)
		close(state.done)
		c.lock.Unlock()

		if recovered != nil {
			panic(recovered)
		}
	}()

	return c.build(concrete, params...)
}

func currentGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	fields := strings.Fields(string(buf[:n]))
	if len(fields) < 2 {
		return 0
	}
	id, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return id
}
