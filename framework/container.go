package framework

import (
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
)

type binding struct {
	concrete   interface{}
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
	stack []resolutionFrame
}

type resolutionFrame struct {
	name string
	node string
}

// Container 提供单例、工厂和显式实例三种依赖绑定方式。
type Container struct {
	initOnce   sync.Once
	state      *containerState
	resolution *resolution
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
func (c *Container) Bind(abstract string, concrete interface{}) {
	c.bind(abstract, concrete, Singleton)
}

// BindFactory 注册每次解析都重新执行的工厂绑定。
func (c *Container) BindFactory(abstract string, concrete interface{}) {
	c.bind(abstract, concrete, Factory)
}

func (c *Container) bind(abstract string, concrete interface{}, lifecycle LifecycleType) {
	state := c.sharedState()
	state.lock.Lock()
	generation := state.nextGenerationLocked(abstract)
	state.bindings[abstract] = binding{
		concrete:   concrete,
		lifecycle:  lifecycle,
		generation: generation,
	}
	delete(state.instances, abstract)
	// 将旧构建从当前索引移除，使新绑定可以立即开始构建；旧构建完成时会因代际不符返回错误。
	delete(state.building, abstract)
	state.lock.Unlock()
}

// Instance 绑定已经创建的显式实例，并使同名旧工厂立即失效。
func (c *Container) Instance(abstract string, instance interface{}) {
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
	state := c.sharedState()
	scope := c.currentResolution()
	if scope.contains(abstract) {
		return nil, scope.circularError(abstract)
	}

	state.lock.Lock()
	if instance, ok := state.instances[abstract]; ok {
		state.lock.Unlock()
		return instance, nil
	}

	registered, ok := state.bindings[abstract]
	if !ok {
		state.lock.Unlock()
		return nil, fmt.Errorf("binding not found: %s", abstract)
	}

	if registered.lifecycle == Factory {
		generation := registered.generation
		node := state.nextNodeLocked(abstract)
		state.lock.Unlock()
		instance, err := c.build(registered.concrete, scope.push(abstract, node), params...)
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

	return c.buildSingleton(abstract, registered.concrete, scope.push(abstract, active.node), active, params...)
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
func (c *Container) Delete(abstract string) {
	state := c.sharedState()
	state.lock.Lock()
	state.nextGenerationLocked(abstract)
	delete(state.bindings, abstract)
	delete(state.instances, abstract)
	delete(state.building, abstract)
	state.lock.Unlock()
}

// build 构建普通值、reflect.Type 或工厂函数。
func (c *Container) build(concrete interface{}, scope *resolution, params ...interface{}) (interface{}, error) {
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

	return c.callFactory(factory, scope, params...)
}

// callFactory 在调用反射工厂前完整校验输入和输出，并把工厂 panic 转换为错误。
func (c *Container) callFactory(factory reflect.Value, scope *resolution, params ...interface{}) (result interface{}, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = nil
			err = fmt.Errorf("工厂执行 panic: %v", recovered)
		}
	}()

	factoryType := factory.Type()
	inputs, err := c.factoryInputs(factoryType, scope, params)
	if err != nil {
		return nil, err
	}

	outputs := factory.Call(inputs)
	if len(outputs) == 0 {
		return nil, fmt.Errorf("工厂必须返回实例或 (实例, error)")
	}
	if len(outputs) > 2 {
		return nil, fmt.Errorf("工厂返回值数量必须为 1 或 2，实际为 %d", len(outputs))
	}
	if len(outputs) == 2 {
		errorType := reflect.TypeOf((*error)(nil)).Elem()
		if !outputs[1].Type().Implements(errorType) {
			return nil, fmt.Errorf("工厂第二个返回值必须实现 error")
		}
		if !outputs[1].IsNil() {
			return nil, outputs[1].Interface().(error)
		}
	}
	return outputs[0].Interface(), nil
}

func (c *Container) factoryInputs(factoryType reflect.Type, scope *resolution, params []interface{}) ([]reflect.Value, error) {
	containerType := reflect.TypeOf((*Container)(nil))
	injectContainer := factoryType.NumIn() > 0 && factoryType.In(0) == containerType
	fixedOffset := 0
	inputs := make([]reflect.Value, 0, factoryType.NumIn()+len(params))
	if injectContainer {
		inputs = append(inputs, reflect.ValueOf(c.scoped(scope)))
		fixedOffset = 1
	}

	required := factoryType.NumIn() - fixedOffset
	if factoryType.IsVariadic() {
		required--
		if len(params) < required {
			return nil, fmt.Errorf("工厂参数不足：至少需要 %d 个，实际为 %d 个", required, len(params))
		}
	} else if len(params) != required {
		return nil, fmt.Errorf("工厂参数数量错误：需要 %d 个，实际为 %d 个", required, len(params))
	}

	for index, parameter := range params {
		typeIndex := index + fixedOffset
		var targetType reflect.Type
		if factoryType.IsVariadic() && typeIndex >= factoryType.NumIn()-1 {
			targetType = factoryType.In(factoryType.NumIn() - 1).Elem()
		} else {
			targetType = factoryType.In(typeIndex)
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
func (c *Container) buildSingleton(abstract string, concrete interface{}, scope *resolution, active *buildState, params ...interface{}) (instance interface{}, err error) {
	instance, err = c.build(concrete, scope, params...)
	if err != nil {
		err = fmt.Errorf("build singleton %s failed: %w", abstract, err)
	}

	state := c.sharedState()
	state.lock.Lock()
	registered, exists := state.bindings[abstract]
	if err == nil && (!exists || registered.generation != active.generation) {
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
	return &Container{state: c.sharedState(), resolution: scope}
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

func (r *resolution) push(abstract string, node string) *resolution {
	stack := make([]resolutionFrame, len(r.stack), len(r.stack)+1)
	copy(stack, r.stack)
	stack = append(stack, resolutionFrame{name: abstract, node: node})
	return &resolution{stack: stack}
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
