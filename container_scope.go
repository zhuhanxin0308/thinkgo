package framework

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
)

// ErrInvalidContainerScopeContext 表示作用域关闭没有收到有效上下文。
var ErrInvalidContainerScopeContext = errors.New("container scope close context cannot be nil")

type scopedInstance struct {
	value      interface{}
	generation uint64
}

type containerScopeState struct {
	lock         sync.Mutex
	instances    map[string]scopedInstance
	building     map[string]*buildState
	waiting      map[string]map[string]struct{}
	created      []interface{}
	nodeSequence uint64
	closing      bool
	closed       bool
	closeErr     error
}

// ContainerScope 是一个并发安全、可关闭的请求或任务级服务作用域。
type ContainerScope struct {
	container        *Container
	state            containerScopeState
	closeLifecycleMu sync.Mutex
	closeStarted     bool
	closeDone        chan struct{}
}

// NewScope 创建共享全局绑定和单例、但隔离 Scoped 实例的新作用域。
func (c *Container) NewScope() *ContainerScope {
	// 普通请求只使用 Factory 和 Singleton；Scoped 索引在首次需要时由同一把锁保护创建。
	// 作用域和它独占的容器副本共用存储，仍由各自请求持有，不跨请求复用或池化。
	storage := &struct {
		ContainerScope
		container Container
	}{}
	scope := &storage.ContainerScope
	scope.container = &storage.container
	storage.container.state = c.sharedState()
	storage.container.lifetimeScope = &scope.state
	return scope
}

// Make 在当前作用域解析服务；Singleton 仍由根容器共享，Factory 保持每次构建。
func (s *ContainerScope) Make(abstract string, params ...interface{}) (interface{}, error) {
	if s == nil || s.container == nil {
		return nil, ErrContainerScopeClosed
	}
	s.state.lock.Lock()
	closed := s.state.closing || s.state.closed
	s.state.lock.Unlock()
	if closed {
		return nil, ErrContainerScopeClosed
	}
	return s.container.Make(abstract, params...)
}

// Get 是 Make 的必需成功版本，解析失败时 panic。
func (s *ContainerScope) Get(abstract string) interface{} {
	instance, err := s.Make(abstract)
	if err != nil {
		panic(err)
	}
	return instance
}

// HasRequestScopedResources 判断作用域是否已经创建或正在创建需要收尾的实例。
func (s *ContainerScope) HasRequestScopedResources() bool {
	if s == nil || s.container == nil {
		return false
	}
	s.state.lock.Lock()
	defer s.state.lock.Unlock()
	return len(s.state.created) != 0 || len(s.state.building) != 0
}

// Close 等待在途构建和全部作用域资源真实关闭。
func (s *ContainerScope) Close() error {
	return s.CloseContext(context.Background())
}

// CloseContext 启动一次完整关闭，并允许调用方按上下文停止等待。
// 旧 io.Closer 无法被 Go 运行时强制终止，因此超时后会继续在受监督任务中运行，
// 监督任务等当前资源真实返回后才按逆序关闭下一项；后续 Close 会等待全部任务真实结束。
func (s *ContainerScope) CloseContext(ctx context.Context) error {
	if s == nil || s.container == nil {
		return nil
	}
	if ctx == nil {
		return ErrInvalidContainerScopeContext
	}
	if idleClosed, idleErr := s.CloseIfIdle(); idleClosed {
		return idleErr
	}
	s.closeLifecycleMu.Lock()
	startClose := !s.closeStarted
	if startClose {
		s.closeStarted = true
		s.closeDone = make(chan struct{})
		s.state.lock.Lock()
		s.state.closing = true
		s.state.lock.Unlock()
	}
	done := s.closeDone
	s.closeLifecycleMu.Unlock()
	if startClose {
		go s.close(ctx, done)
	}
	if done == nil {
		s.state.lock.Lock()
		closeErr := s.state.closeErr
		s.state.lock.Unlock()
		return closeErr
	}
	select {
	case <-done:
		return s.scopeCloseError()
	case <-ctx.Done():
		select {
		case <-done:
			return s.scopeCloseError()
		default:
			return ctx.Err()
		}
	}
}

// CloseIfIdle 在作用域没有在途构建和已创建实例时零分配关闭。
// Request 用它保持无 Scoped 服务请求的热路径；返回 false 时调用方必须进入完整 CloseContext。
func (s *ContainerScope) CloseIfIdle() (bool, error) {
	if s == nil || s.container == nil {
		return true, nil
	}
	s.closeLifecycleMu.Lock()
	defer s.closeLifecycleMu.Unlock()
	if s.closeStarted {
		return false, nil
	}
	s.state.lock.Lock()
	defer s.state.lock.Unlock()
	if s.state.closed {
		return true, s.state.closeErr
	}
	if s.state.closing || len(s.state.building) != 0 || len(s.state.created) != 0 {
		return false, nil
	}
	s.closeStarted = true
	s.state.closing = true
	s.state.closed = true
	s.state.instances = nil
	s.state.created = nil
	return true, s.state.closeErr
}

func (s *ContainerScope) close(ctx context.Context, done chan struct{}) {
	state := &s.state
	var closeErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("关闭容器作用域 panic: %v", recovered))
		}
		state.lock.Lock()
		state.closeErr = closeErr
		close(done)
		state.lock.Unlock()
	}()
	state.lock.Lock()
	active := make([]*buildState, 0, len(state.building))
	for _, build := range state.building {
		active = append(active, build)
	}
	state.lock.Unlock()
	for _, build := range active {
		<-build.done
	}

	state.lock.Lock()
	instances := make([]interface{}, 0, len(state.created))
	for index := len(state.created) - 1; index >= 0; index-- {
		instances = append(instances, state.created[index])
	}
	state.instances = nil
	state.created = nil
	state.closed = true
	state.lock.Unlock()

	closed := make(map[string]struct{}, len(instances))
	for _, instance := range instances {
		closer, ok := instance.(io.Closer)
		if !ok || isNilServiceInstance(closer) {
			continue
		}
		identity := scopedCloserIdentity(closer)
		if _, exists := closed[identity]; exists {
			continue
		}
		closed[identity] = struct{}{}
		closeErr = errors.Join(closeErr, closeScopedServiceContext(ctx, closer))
	}
}

func (s *ContainerScope) scopeCloseError() error {
	s.state.lock.Lock()
	defer s.state.lock.Unlock()
	return s.state.closeErr
}

func (c *Container) makeScoped(abstract string, registered binding, resolutionScope *resolution, params ...interface{}) (interface{}, error) {
	state := c.lifetimeScope
	if state == nil {
		return nil, fmt.Errorf("%w: %s", ErrContainerScopeRequired, abstract)
	}
	state.lock.Lock()
	if state.closing || state.closed {
		state.lock.Unlock()
		return nil, ErrContainerScopeClosed
	}
	if instance, ok := state.instances[abstract]; ok && instance.generation == registered.generation {
		state.lock.Unlock()
		return instance.value, nil
	}
	delete(state.instances, abstract)
	if active, ok := state.building[abstract]; ok {
		from := resolutionScope.current()
		if err := addScopeWaitLocked(state, from.node, active.node, from.name, abstract); err != nil {
			state.lock.Unlock()
			return nil, err
		}
		state.lock.Unlock()
		<-active.done
		state.lock.Lock()
		removeScopeWaitLocked(state, from.node, active.node)
		state.lock.Unlock()
		return active.instance, active.err
	}
	state.nodeSequence++
	active := &buildState{
		done:       make(chan struct{}),
		generation: registered.generation,
		node:       fmt.Sprintf("scope:%s@%d", abstract, state.nodeSequence),
	}
	if state.building == nil {
		state.building = make(map[string]*buildState)
	}
	state.building[abstract] = active
	state.lock.Unlock()

	instance, err := c.build(registered, resolutionScope.push(abstract, active.node, Scoped), params...)
	constructed := err == nil
	if err != nil {
		err = fmt.Errorf("build scoped %s failed: %w", abstract, err)
	}
	global := c.sharedState()
	global.lock.RLock()
	current, exists := global.bindings[abstract]
	if err == nil && (!exists || current.generation != registered.generation || current.lifecycle != Scoped) {
		err = bindingChangedError(abstract)
	}

	state.lock.Lock()
	// 工厂成功后所有权已经交给作用域；即使绑定失效，也必须保留到统一逆序关闭。
	if constructed {
		state.created = append(state.created, instance)
	}
	active.err = err
	if err == nil {
		active.instance = instance
		if state.instances == nil {
			state.instances = make(map[string]scopedInstance)
		}
		state.instances[abstract] = scopedInstance{value: instance, generation: registered.generation}
	}
	if currentBuild, ok := state.building[abstract]; ok && currentBuild == active {
		delete(state.building, abstract)
	}
	close(active.done)
	state.lock.Unlock()
	global.lock.RUnlock()
	return instance, err
}

func addScopeWaitLocked(state *containerScopeState, fromNode, toNode, fromName, toName string) error {
	if fromNode == "" {
		return nil
	}
	if state.waiting == nil {
		state.waiting = make(map[string]map[string]struct{})
	}
	if state.waiting[fromNode] == nil {
		state.waiting[fromNode] = make(map[string]struct{})
	}
	state.waiting[fromNode][toNode] = struct{}{}
	if hasScopeWaitPathLocked(state, toNode, fromNode, make(map[string]bool)) {
		removeScopeWaitLocked(state, fromNode, toNode)
		return fmt.Errorf("circular scoped dependency detected: %s -> %s", fromName, toName)
	}
	return nil
}

func removeScopeWaitLocked(state *containerScopeState, from, to string) {
	if from == "" {
		return
	}
	delete(state.waiting[from], to)
	if len(state.waiting[from]) == 0 {
		delete(state.waiting, from)
	}
}

func hasScopeWaitPathLocked(state *containerScopeState, current, target string, visited map[string]bool) bool {
	if current == target {
		return true
	}
	if visited[current] {
		return false
	}
	visited[current] = true
	for next := range state.waiting[current] {
		if hasScopeWaitPathLocked(state, next, target, visited) {
			return true
		}
	}
	return false
}

func scopedCloserIdentity(closer io.Closer) string {
	value := reflect.ValueOf(closer)
	if value.IsValid() {
		switch value.Kind() {
		case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice:
			if !value.IsNil() {
				return fmt.Sprintf("%T:%x", closer, value.Pointer())
			}
		}
	}
	return fmt.Sprintf("%T:%v", closer, closer)
}

func closeScopedServiceContext(ctx context.Context, closer io.Closer) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("关闭作用域服务 panic: %v", recovered)
		}
	}()
	if contextual, ok := closer.(interface {
		CloseContext(context.Context) error
	}); ok {
		contextErr := contextual.CloseContext(ctx)
		if ctx.Err() != nil && errors.Is(contextErr, ctx.Err()) {
			return closer.Close()
		}
		return contextErr
	}
	return closer.Close()
}
