package framework

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// LifecycleType 表示容器绑定的生命周期。
type LifecycleType int

const (
	// Singleton 在首次解析后缓存实例。
	Singleton LifecycleType = iota
	// Factory 在每次解析时创建新实例。
	Factory
	// Scoped 在一个显式请求或任务作用域内只创建一次实例。
	Scoped
)

var (
	// ErrContainerScopeRequired 表示 Scoped 绑定不能从根容器解析。
	ErrContainerScopeRequired = errors.New("container scope is required")
	// ErrContainerScopeClosed 表示请求或任务作用域已经进入关闭阶段。
	ErrContainerScopeClosed = errors.New("container scope is closed")
	// ErrScopedDependencyCaptured 表示长生命周期单例试图持有请求或任务作用域服务。
	ErrScopedDependencyCaptured = errors.New("singleton cannot capture scoped dependency")
)

type binding struct {
	concrete   interface{}
	factory    *factoryPlan
	lifecycle  LifecycleType
	generation uint64
}

// buildState 保存一次单例构建的完成信号和最终结果。
type buildState struct {
	done       chan struct{}
	instance   interface{}
	err        error
	generation uint64
	node       string
}

// containerState 是所有作用域容器共享的并发状态。
type containerState struct {
	ownerLock    sync.RWMutex
	owner        *App
	lock         sync.RWMutex
	bindings     map[string]binding
	instances    map[string]interface{}
	building     map[string]*buildState
	generations  map[string]uint64
	waiting      map[string]map[string]struct{}
	nodeSequence uint64
}

// resolution 保存当前依赖解析链；作用域副本只共享不可变切片。
type resolution struct {
	stack   []resolutionFrame
	context context.Context
}

type resolutionFrame struct {
	name      string
	node      string
	lifecycle LifecycleType
}

// Container 提供单例、工厂和显式实例三种依赖绑定方式。
type Container struct {
	initOnce      sync.Once
	state         *containerState
	resolution    *resolution
	lifetimeScope *containerScopeState
}

// NewContainer 创建并初始化服务容器。
func NewContainer() *Container {
	return &Container{state: newContainerState()}
}

func newContainerState() *containerState {
	return &containerState{
		bindings:    make(map[string]binding),
		instances:   make(map[string]interface{}),
		building:    make(map[string]*buildState),
		generations: make(map[string]uint64),
		waiting:     make(map[string]map[string]struct{}),
	}
}

// Bind 注册单例绑定；同名重绑定会使旧实例和在途构建失效。
//
// 归属应用的容器在生命周期拒绝变更时会 panic；需要显式处理错误的调用方应使用 TryBind。
func (c *Container) Bind(abstract string, concrete interface{}) {
	mustContainerMutation(c.TryBind(abstract, concrete))
}

// BindFactory 注册每次解析都重新执行的工厂绑定。
func (c *Container) BindFactory(abstract string, concrete interface{}) {
	mustContainerMutation(c.TryBindFactory(abstract, concrete))
}

// BindScoped 注册在每个显式作用域内只构建一次的绑定。
func (c *Container) BindScoped(abstract string, concrete interface{}) {
	mustContainerMutation(c.TryBindScoped(abstract, concrete))
}

func (c *Container) bindDirect(abstract string, concrete interface{}, lifecycle LifecycleType) {
	factory := compileFactoryPlan(concrete)
	state := c.sharedState()
	state.lock.Lock()
	generation := state.nextGenerationLocked(abstract)
	state.bindings[abstract] = binding{
		concrete:   concrete,
		factory:    factory,
		lifecycle:  lifecycle,
		generation: generation,
	}
	delete(state.instances, abstract)
	// 将旧构建从当前索引移除，使新绑定可以立即开始构建；旧构建完成时会因代际不符返回错误。
	delete(state.building, abstract)
	state.lock.Unlock()
}

// Instance 绑定已经创建的显式实例，并使同名旧工厂立即失效。
//
// 归属应用的容器在生命周期或内置服务契约拒绝变更时会 panic；
// 需要显式处理错误的调用方应使用 TryInstance。
func (c *Container) Instance(abstract string, instance interface{}) {
	mustContainerMutation(c.TryInstance(abstract, instance))
}

func (c *Container) instanceDirect(abstract string, instance interface{}) {
	state := c.sharedState()
	state.lock.Lock()
	generation := state.nextGenerationLocked(abstract)
	state.bindings[abstract] = binding{
		concrete:   instance,
		lifecycle:  Singleton,
		generation: generation,
	}
	state.instances[abstract] = instance
	delete(state.building, abstract)
	state.lock.Unlock()
}

// Make 解析服务；并发解析同名单例时共享一次构建结果。
func (c *Container) Make(abstract string, params ...interface{}) (interface{}, error) {
	return c.makeWithResolution(c.currentResolution(), abstract, params...)
}

// LookupInstance 只读取已经发布的单例或显式实例，不执行工厂，也不等待在途构建。
// 资源关闭和状态检查使用此入口，避免检查行为触发新的外部连接。
func (c *Container) LookupInstance(abstract string) (interface{}, bool) {
	if c == nil {
		return nil, false
	}
	state := c.sharedState()
	state.lock.RLock()
	instance, exists := state.instances[abstract]
	state.lock.RUnlock()
	return instance, exists
}

// makeWithResolution 直接传递解析状态，只在工厂实际注入容器时创建容器副本。
func (c *Container) makeWithResolution(scope *resolution, abstract string, params ...interface{}) (interface{}, error) {
	state := c.sharedState()
	if scope.contains(abstract) {
		return nil, scope.circularError(abstract)
	}

	state.lock.RLock()
	if instance, ok := state.instances[abstract]; ok {
		state.lock.RUnlock()
		return instance, nil
	}

	registered, ok := state.bindings[abstract]
	if !ok {
		state.lock.RUnlock()
		return nil, fmt.Errorf("binding not found: %s", abstract)
	}

	if registered.lifecycle == Scoped {
		state.lock.RUnlock()
		if scope.containsLifecycle(Singleton) {
			return nil, fmt.Errorf("%w: %s", ErrScopedDependencyCaptured, abstract)
		}
		return c.makeScoped(abstract, registered, scope, params...)
	}
	if registered.lifecycle == Factory {
		generation := registered.generation
		state.lock.RUnlock()
		// Factory 不参与单例等待图，使用空节点即可避免为每次请求获取排他锁。
		instance, err := c.build(registered, scope.push(abstract, "", Factory), params...)
		if err != nil {
			return nil, fmt.Errorf("build factory %s failed: %w", abstract, err)
		}
		state.lock.RLock()
		currentGeneration := state.generations[abstract]
		state.lock.RUnlock()
		if currentGeneration != generation {
			return nil, bindingChangedError(abstract)
		}
		return instance, nil
	}
	state.lock.RUnlock()

	// 单例路径需要创建等待状态，重新获取排他锁并复核绑定，防止读锁释放期间发生重绑定。
	state.lock.Lock()
	if instance, ok := state.instances[abstract]; ok {
		state.lock.Unlock()
		return instance, nil
	}
	registered, ok = state.bindings[abstract]
	if !ok {
		state.lock.Unlock()
		return nil, fmt.Errorf("binding not found: %s", abstract)
	}
	if registered.lifecycle == Factory {
		state.lock.Unlock()
		return c.makeWithResolution(scope, abstract, params...)
	}
	if registered.lifecycle == Scoped {
		state.lock.Unlock()
		if scope.containsLifecycle(Singleton) {
			return nil, fmt.Errorf("%w: %s", ErrScopedDependencyCaptured, abstract)
		}
		return c.makeScoped(abstract, registered, scope, params...)
	}

	if active, ok := state.building[abstract]; ok {
		from := scope.current()
		if err := state.addWaitLocked(from.node, active.node, from.name, abstract); err != nil {
			state.lock.Unlock()
			return nil, err
		}
		state.lock.Unlock()
		<-active.done
		state.lock.Lock()
		state.removeWaitLocked(from.node, active.node)
		state.lock.Unlock()
		return active.instance, active.err
	}

	active := &buildState{
		done:       make(chan struct{}),
		generation: registered.generation,
		node:       state.nextNodeLocked(abstract),
	}
	state.building[abstract] = active
	state.lock.Unlock()

	return c.buildSingleton(abstract, registered, scope.push(abstract, active.node, Singleton), active, params...)
}

// Get 是 Make 的必需成功版本，解析失败时 panic。
func (c *Container) Get(abstract string) interface{} {
	instance, err := c.Make(abstract)
	if err != nil {
		panic(err)
	}
	return instance
}

// Has 判断服务是否存在绑定或显式实例。
func (c *Container) Has(abstract string) bool {
	state := c.sharedState()
	state.lock.RLock()
	_, hasInstance := state.instances[abstract]
	_, hasBinding := state.bindings[abstract]
	state.lock.RUnlock()
	return hasInstance || hasBinding
}

// Delete 删除绑定和实例，并使对应在途构建失效。
//
// 归属应用的容器在生命周期或内置服务契约拒绝变更时会 panic；
// 需要显式处理错误的调用方应使用 TryDelete。
func (c *Container) Delete(abstract string) {
	mustContainerMutation(c.TryDelete(abstract))
}

func (c *Container) deleteDirect(abstract string) {
	state := c.sharedState()
	state.lock.Lock()
	state.nextGenerationLocked(abstract)
	delete(state.bindings, abstract)
	delete(state.instances, abstract)
	delete(state.building, abstract)
	state.lock.Unlock()
}

// build 构建普通值、reflect.Type 或工厂函数。
func (c *Container) build(registered binding, scope *resolution, params ...interface{}) (interface{}, error) {
	if registered.factory != nil {
		return c.callFactory(registered.factory, scope, params)
	}
	concrete := registered.concrete
	if targetType, ok := concrete.(reflect.Type); ok {
		if targetType == nil {
			return nil, fmt.Errorf("reflect.Type 不能为空")
		}
		return reflect.New(targetType).Interface(), nil
	}

	factory := reflect.ValueOf(concrete)
	if !factory.IsValid() {
		return nil, fmt.Errorf("绑定实现不能为空")
	}
	if factory.Kind() != reflect.Func {
		return concrete, nil
	}

	return c.callFactory(compileFactoryPlan(concrete), scope, params)
}

// callFactory 统一校验调用计划并转换 panic，直接调用与反射调用共用生命周期错误边界。
func (c *Container) callFactory(plan *factoryPlan, scope *resolution, params []interface{}) (result interface{}, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = nil
			err = fmt.Errorf("工厂执行 panic: %v", recovered)
		}
	}()

	if err := plan.validateParameterCount(len(params)); err != nil {
		return nil, err
	}
	if plan.directCall != nil {
		return plan.directCall(c, scope, params)
	}
	inputs, err := c.factoryInputs(plan, scope, params)
	if err != nil {
		return nil, err
	}

	outputs := plan.factory.Call(inputs)
	if plan.outputError != nil {
		return nil, plan.outputError
	}
	if len(outputs) == 2 {
		if reflectErrorOutputIsNil(outputs[1]) {
			return outputs[0].Interface(), nil
		}
		return outputs[0].Interface(), outputs[1].Interface().(error)
	}
	return outputs[0].Interface(), nil
}

// reflectErrorOutputIsNil 只把 nil 接口和 nil 指针视为无错误，避免对具体值类型调用 IsNil 导致反射 panic。
func reflectErrorOutputIsNil(output reflect.Value) bool {
	switch output.Kind() {
	case reflect.Interface, reflect.Pointer:
		return output.IsNil()
	default:
		return false
	}
}

func (c *Container) factoryInputs(plan *factoryPlan, scope *resolution, params []interface{}) ([]reflect.Value, error) {
	inputCount := len(params)
	if plan.injectContainer {
		inputCount++
	}
	inputs := make([]reflect.Value, 0, inputCount)
	if plan.injectContainer {
		inputs = append(inputs, reflect.ValueOf(c.scoped(scope)))
	}
	for index, parameter := range params {
		targetType := plan.variadicType
		if index < len(plan.fixedTypes) {
			targetType = plan.fixedTypes[index]
		}
		value, err := reflectArgument(parameter, targetType)
		if err != nil {
			return nil, fmt.Errorf("工厂第 %d 个参数无效: %w", index+1, err)
		}
		inputs = append(inputs, value)
	}
	return inputs, nil
}

func reflectArgument(parameter interface{}, targetType reflect.Type) (reflect.Value, error) {
	if parameter == nil {
		switch targetType.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
			return reflect.Zero(targetType), nil
		default:
			return reflect.Value{}, fmt.Errorf("nil 不能赋给 %s", targetType)
		}
	}

	value := reflect.ValueOf(parameter)
	if !value.Type().AssignableTo(targetType) {
		return reflect.Value{}, fmt.Errorf("%s 不能赋给 %s", value.Type(), targetType)
	}
	return value, nil
}

// buildSingleton 提交构建结果前核对绑定代际，阻止删除或重绑定后的旧结果复活。
func (c *Container) buildSingleton(abstract string, registered binding, scope *resolution, active *buildState, params ...interface{}) (instance interface{}, err error) {
	instance, err = c.build(registered, scope, params...)
	if err != nil {
		err = fmt.Errorf("build singleton %s failed: %w", abstract, err)
	}

	state := c.sharedState()
	state.lock.Lock()
	currentBinding, exists := state.bindings[abstract]
	if err == nil && (!exists || currentBinding.generation != active.generation) {
		err = bindingChangedError(abstract)
	}
	active.err = err
	if err == nil {
		active.instance = instance
		state.instances[abstract] = instance
	}
	if current, ok := state.building[abstract]; ok && current == active {
		delete(state.building, abstract)
	}
	close(active.done)
	state.lock.Unlock()
	return instance, err
}

func bindingChangedError(abstract string) error {
	return fmt.Errorf("binding changed while building: %s", abstract)
}

func (c *Container) sharedState() *containerState {
	c.initOnce.Do(func() {
		if c.state == nil {
			c.state = newContainerState()
		}
	})
	return c.state
}

func (c *Container) currentResolution() *resolution {
	if c.resolution == nil {
		return &resolution{}
	}
	return c.resolution
}

func (c *Container) scoped(scope *resolution) *Container {
	return &Container{state: c.sharedState(), resolution: scope, lifetimeScope: c.lifetimeScope}
}

func (r *resolution) contains(abstract string) bool {
	for _, current := range r.stack {
		if current.name == abstract {
			return true
		}
	}
	return false
}

func (r *resolution) current() resolutionFrame {
	if len(r.stack) == 0 {
		return resolutionFrame{}
	}
	return r.stack[len(r.stack)-1]
}

func (r *resolution) push(abstract string, node string, lifecycle LifecycleType) *resolution {
	if len(r.stack) == 0 {
		// 首层解析状态与唯一栈帧共用一次分配；发布后仍保持不可变，嵌套解析继续复制栈。
		next := &struct {
			resolution
			frame [1]resolutionFrame
		}{}
		next.frame[0] = resolutionFrame{name: abstract, node: node, lifecycle: lifecycle}
		next.resolution = resolution{stack: next.frame[:], context: r.context}
		return &next.resolution
	}
	stack := make([]resolutionFrame, len(r.stack), len(r.stack)+1)
	copy(stack, r.stack)
	stack = append(stack, resolutionFrame{name: abstract, node: node, lifecycle: lifecycle})
	return &resolution{stack: stack, context: r.context}
}

func (r *resolution) containsLifecycle(lifecycle LifecycleType) bool {
	for _, frame := range r.stack {
		if frame.lifecycle == lifecycle {
			return true
		}
	}
	return false
}

func (r *resolution) circularError(abstract string) error {
	chain := make([]string, 0, len(r.stack)+1)
	for _, frame := range r.stack {
		chain = append(chain, frame.name)
	}
	chain = append(chain, abstract)
	return fmt.Errorf("circular singleton dependency detected: %s", strings.Join(chain, " -> "))
}

func (s *containerState) nextGenerationLocked(abstract string) uint64 {
	s.generations[abstract]++
	return s.generations[abstract]
}

func (s *containerState) nextNodeLocked(abstract string) string {
	s.nodeSequence++
	return fmt.Sprintf("%s@%d", abstract, s.nodeSequence)
}

func (s *containerState) addWaitLocked(fromNode string, toNode string, fromName string, toName string) error {
	if fromNode == "" {
		return nil
	}
	if s.waiting[fromNode] == nil {
		s.waiting[fromNode] = make(map[string]struct{})
	}
	s.waiting[fromNode][toNode] = struct{}{}
	if s.hasWaitPathLocked(toNode, fromNode, make(map[string]bool)) {
		s.removeWaitLocked(fromNode, toNode)
		return fmt.Errorf("circular singleton dependency detected: %s -> %s", fromName, toName)
	}
	return nil
}

func (s *containerState) removeWaitLocked(from string, to string) {
	if from == "" {
		return
	}
	delete(s.waiting[from], to)
	if len(s.waiting[from]) == 0 {
		delete(s.waiting, from)
	}
}

func (s *containerState) hasWaitPathLocked(current string, target string, visited map[string]bool) bool {
	if current == target {
		return true
	}
	if visited[current] {
		return false
	}
	visited[current] = true
	for next := range s.waiting[current] {
		if s.hasWaitPathLocked(next, target, visited) {
			return true
		}
	}
	return false
}
