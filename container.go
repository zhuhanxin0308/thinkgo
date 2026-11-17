package framework

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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
	// ErrContainerBuildAborted 表示构建工厂所在的 goroutine 在返回前退出，等待者可以重新解析服务。
	ErrContainerBuildAborted = errors.New("container service build exited before returning")
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

// resolution 保存当前依赖解析链；作用域副本只共享不可变切片，已完成构建帧通过原子标记退役。
type resolution struct {
	stack           []resolutionFrame
	context         context.Context
	contextDone     *atomic.Bool
	singletonOrigin bool
}

type resolutionFrame struct {
	name      string
	node      string
	lifecycle LifecycleType
	done      *atomic.Bool
}

func (f resolutionFrame) isActive() bool {
	return f.done == nil || !f.done.Load()
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
		addedNodes, err := state.addWaitEdgesLocked(scope, active.node, abstract)
		if err != nil {
			state.lock.Unlock()
			return nil, err
		}
		state.lock.Unlock()
		<-active.done
		state.lock.Lock()
		state.removeWaitEdgesLocked(addedNodes, active.node)
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

	return c.buildSingleton(abstract, registered, scope, active, params...)
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
	if plan.outputError != nil {
		return nil, plan.outputError
	}
	if plan.directCall != nil {
		return plan.directCall(c, scope, params)
	}
	inputs, err := c.factoryInputs(plan, scope, params)
	if err != nil {
		return nil, err
	}

	outputs := plan.factory.Call(inputs)
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

// buildSingleton 提交构建结果前核对绑定代际，阻止删除或重绑定后的旧结果复活；
// 构建退出后自动退役本次单例帧与构建上下文，并剥离调用方的临时请求作用域。
func (c *Container) buildSingleton(abstract string, registered binding, scope *resolution, active *buildState, params ...interface{}) (instance interface{}, err error) {
	state := c.sharedState()
	frameDone := &atomic.Bool{}
	singletonScope := scope.pushWithDone(abstract, active.node, Singleton, frameDone)
	builder := c.withoutLifetimeScope()
	returned := false
	defer func() {
		frameDone.Store(true)
		// Goexit 会运行 defer，但不会让工厂调用返回。
		if !returned {
			err = fmt.Errorf("build singleton %s failed: %w", abstract, ErrContainerBuildAborted)
		}
		state.lock.Lock()
		defer state.lock.Unlock()
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
	}()
	instance, err = builder.build(registered, singletonScope, params...)
	returned = true
	if err != nil {
		err = fmt.Errorf("build singleton %s failed: %w", abstract, err)
	}
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

var emptyResolution resolution

func (c *Container) currentResolution() *resolution {
	if c.resolution == nil {
		return &emptyResolution
	}
	return c.resolution
}

func (c *Container) scoped(scope *resolution) *Container {
	return &Container{state: c.sharedState(), resolution: scope, lifetimeScope: c.lifetimeScope}
}

func (c *Container) withoutLifetimeScope() *Container {
	if c.lifetimeScope == nil {
		return c
	}
	return &Container{state: c.sharedState(), resolution: c.resolution}
}

func (r *resolution) effectiveContext() context.Context {
	if r == nil || r.context == nil {
		return nil
	}
	if r.contextDone != nil && r.contextDone.Load() {
		return nil
	}
	return r.context
}

func (r *resolution) activeContextDone() *atomic.Bool {
	if r == nil || r.contextDone == nil || r.contextDone.Load() {
		return nil
	}
	return r.contextDone
}

func (r *resolution) enclosingDone() *atomic.Bool {
	if r == nil {
		return nil
	}
	for index := len(r.stack) - 1; index >= 0; index-- {
		frame := r.stack[index]
		if frame.done != nil && !frame.done.Load() {
			return frame.done
		}
	}
	return nil
}

func (r *resolution) contains(abstract string) bool {
	for _, current := range r.stack {
		if current.isActive() && current.name == abstract {
			return true
		}
	}
	return false
}

func (r *resolution) push(abstract string, node string, lifecycle LifecycleType) *resolution {
	return r.pushWithDone(abstract, node, lifecycle, r.enclosingDone())
}

func (r *resolution) pushWithDone(abstract string, node string, lifecycle LifecycleType, done *atomic.Bool) *resolution {
	ctx := r.effectiveContext()
	ctxDone := r.activeContextDone()
	if done != nil {
		ctxDone = done
	}
	singletonOrigin := r.singletonOrigin || lifecycle == Singleton

	activeCount := 0
	for _, frame := range r.stack {
		if frame.isActive() {
			activeCount++
		}
	}
	if activeCount == 0 {
		// 首层解析状态与唯一栈帧共用一次分配；发布后仍保持不可变，嵌套解析继续复制栈。
		next := &struct {
			resolution
			frame [1]resolutionFrame
		}{}
		next.frame[0] = resolutionFrame{name: abstract, node: node, lifecycle: lifecycle, done: done}
		next.resolution = resolution{
			stack:           next.frame[:],
			context:         ctx,
			contextDone:     ctxDone,
			singletonOrigin: singletonOrigin,
		}
		return &next.resolution
	}
	stack := make([]resolutionFrame, 0, activeCount+1)
	for _, frame := range r.stack {
		if frame.isActive() {
			stack = append(stack, frame)
		}
	}
	stack = append(stack, resolutionFrame{name: abstract, node: node, lifecycle: lifecycle, done: done})
	return &resolution{
		stack:           stack,
		context:         ctx,
		contextDone:     ctxDone,
		singletonOrigin: singletonOrigin,
	}
}

func (r *resolution) containsLifecycle(lifecycle LifecycleType) bool {
	if lifecycle == Singleton && r.singletonOrigin {
		return true
	}
	for _, frame := range r.stack {
		if frame.isActive() && frame.lifecycle == lifecycle {
			return true
		}
	}
	return false
}

func (r *resolution) circularError(abstract string) error {
	chain := make([]string, 0, len(r.stack)+1)
	for _, frame := range r.stack {
		if frame.isActive() {
			chain = append(chain, frame.name)
		}
	}
	chain = append(chain, abstract)
	return fmt.Errorf("circular singleton dependency detected: %s", strings.Join(chain, " -> "))
}

func (r *resolution) circularWaitError(fromNode string, toName string, scoped bool) error {
	chain := make([]string, 0, len(r.stack)+1)
	started := false
	for _, frame := range r.stack {
		if !frame.isActive() {
			continue
		}
		if frame.node == fromNode {
			started = true
		}
		if started {
			chain = append(chain, frame.name)
		}
	}
	if len(chain) == 0 {
		chain = append(chain, fromNode)
	}
	chain = append(chain, toName)
	kind := "singleton"
	if scoped {
		kind = "scoped"
	}
	return fmt.Errorf("circular %s dependency detected: %s", kind, strings.Join(chain, " -> "))
}

func (s *containerState) nextGenerationLocked(abstract string) uint64 {
	s.generations[abstract]++
	return s.generations[abstract]
}

func (s *containerState) nextNodeLocked(abstract string) string {
	s.nodeSequence++
	return fmt.Sprintf("%s@%d", abstract, s.nodeSequence)
}

// addWaitEdgesLocked 将当前解析链上所有未完成单例节点记为等待源，
// 防止同协程嵌套构建或经由 Factory 中转时丢失持有边导致跨协程死锁。
func (s *containerState) addWaitEdgesLocked(scope *resolution, toNode string, toName string) ([]string, error) {
	if scope == nil || toNode == "" {
		return nil, nil
	}
	targets := make(map[string]struct{})
	var sourceNodes []string
	for _, frame := range scope.stack {
		if frame.isActive() && frame.lifecycle == Singleton && frame.node != "" {
			if _, seen := targets[frame.node]; !seen {
				targets[frame.node] = struct{}{}
				sourceNodes = append(sourceNodes, frame.node)
			}
		}
	}
	if len(sourceNodes) == 0 {
		return nil, nil
	}
	if hitNode, found := findReachableWaitTarget(s.waiting, toNode, targets, make(map[string]bool)); found {
		return nil, scope.circularWaitError(hitNode, toName, false)
	}
	added := make([]string, 0, len(sourceNodes))
	for _, fromNode := range sourceNodes {
		if s.waiting[fromNode] == nil {
			s.waiting[fromNode] = make(map[string]struct{})
		}
		if _, exists := s.waiting[fromNode][toNode]; !exists {
			s.waiting[fromNode][toNode] = struct{}{}
			added = append(added, fromNode)
		}
	}
	return added, nil
}

func (s *containerState) removeWaitEdgesLocked(fromNodes []string, toNode string) {
	for _, from := range fromNodes {
		delete(s.waiting[from], toNode)
		if len(s.waiting[from]) == 0 {
			delete(s.waiting, from)
		}
	}
}

func findReachableWaitTarget(waiting map[string]map[string]struct{}, current string, targets map[string]struct{}, visited map[string]bool) (string, bool) {
	if _, matched := targets[current]; matched {
		return current, true
	}
	if visited[current] {
		return "", false
	}
	visited[current] = true
	for next := range waiting[current] {
		if hit, found := findReachableWaitTarget(waiting, next, targets, visited); found {
			return hit, true
		}
	}
	return "", false
}
