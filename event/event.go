package event

import (
	"context"
	"errors"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

const maxEventNameBytes = 256

// maxResolvedEventCacheEntries 限制事件解析缓存的容量，避免动态事件名导致缓存无限增长。
const maxResolvedEventCacheEntries = 256

var (
	// ErrInvalidEvent 表示分发的事件对象为空或不可用。
	ErrInvalidEvent = errors.New("无效事件")
	// ErrInvalidEventName 表示事件名为空、过长、包含控制字符或通配语法错误。
	ErrInvalidEventName = errors.New("无效事件名称")
	// ErrInvalidListener 表示监听器或优先级配置无效。
	ErrInvalidListener = errors.New("无效事件监听器")
	// ErrInvalidSubscriber 表示订阅者为空或不可用。
	ErrInvalidSubscriber = errors.New("无效事件订阅者")
	// ErrInvalidEventBinding 表示事件别名或事件工厂不可用。
	ErrInvalidEventBinding = errors.New("无效事件绑定")
	// ErrEventCallbackPanic 表示事件或回调发生 panic，已被调度器隔离。
	ErrEventCallbackPanic = errors.New("事件回调发生 panic")
	// ErrInvalidEventContext 表示事件分发没有提供有效上下文。
	ErrInvalidEventContext = errors.New("事件分发上下文无效")
)

// Event 定义事件对象最小接口。
type Event interface {
	Name() string
}

// Factory 根据触发参数创建事件对象，对应 ThinkPHP 事件别名指向的事件类。
// 工厂必须能够接收 nil，框架会在注册时用它确认真实事件名称。
type Factory func(data interface{}) Event

// Listener 定义事件监听器。
type Listener interface {
	Handle(event Event) error
}

// ContextualListener 为需要感知取消信号的事件监听器提供可选上下文接口。
type ContextualListener interface {
	HandleContext(ctx context.Context, event Event) error
}

// Subscriber 允许一个对象批量注册多个监听器。
type Subscriber interface {
	Subscribe(dispatcher *Dispatcher) error
}

type listenerEntry struct {
	listener    Listener
	priority    int
	sequence    int64
	application bool
}

// ListenerHandle 表示单个监听器的所有权句柄。
// 动态扩展应保存该句柄并在卸载时调用 Remove，避免清空同一事件上的其它监听器。
type ListenerHandle struct {
	dispatcher *Dispatcher
	eventName  string
	sequence   int64
	lock       sync.Mutex
	removed    bool
	err        error
}

type eventBinding struct {
	factory Factory
	target  string
}

// Dispatcher 负责注册监听器、订阅者以及事件分发。
type Dispatcher struct {
	listeners          map[string][]listenerEntry
	bindings           map[string]eventBinding
	wildcards          map[string]struct{}
	resolved           map[string][]listenerEntry
	resolutionEpoch    uint64
	subscriptionLock   sync.Mutex
	lock               sync.RWMutex
	sequence           int64
	transactionOwner   *Dispatcher
	transactionHandles []*ListenerHandle
}

// dispatcherRegistrationState 保存可由订阅回调修改的完整注册状态。
// 每个订阅先在独立副本上执行，成功后再一次性发布，避免部分状态泄漏和并发事务互相回滚。
type dispatcherRegistrationState struct {
	listeners map[string][]listenerEntry
	bindings  map[string]eventBinding
	wildcards map[string]struct{}
	sequence  int64
}

// NewDispatcher 创建事件调度器。
func NewDispatcher() *Dispatcher {
	dispatcher := &Dispatcher{
		listeners: make(map[string][]listenerEntry),
		bindings:  make(map[string]eventBinding),
		wildcards: make(map[string]struct{}),
		resolved:  make(map[string][]listenerEntry),
	}
	for alias, factory := range builtInEventFactories() {
		binding, err := prepareEventBinding(alias, factory)
		if err == nil {
			dispatcher.bindings[alias] = binding
		}
	}
	return dispatcher
}

// Bind 批量注册事件别名，对应 ThinkPHP Event.bind。
func (d *Dispatcher) Bind(events map[string]Factory) error {
	if d == nil {
		return fmt.Errorf("%w: 调度器不能为空", ErrInvalidEventBinding)
	}
	aliases := make([]string, 0, len(events))
	for alias := range events {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)

	prepared := make(map[string]eventBinding, len(events))
	for _, alias := range aliases {
		binding, err := prepareEventBinding(alias, events[alias])
		if err != nil {
			return err
		}
		prepared[alias] = binding
	}

	d.subscriptionLock.Lock()
	defer d.subscriptionLock.Unlock()
	d.lock.Lock()
	if d.bindings == nil {
		d.bindings = make(map[string]eventBinding)
	}
	for _, alias := range aliases {
		d.bindings[alias] = prepared[alias]
	}
	d.lock.Unlock()
	return nil
}

// BindEvent 注册一个事件别名，便于需要动态装配的 Go 代码使用。
func (d *Dispatcher) BindEvent(alias string, factory Factory) error {
	return d.Bind(map[string]Factory{alias: factory})
}

// ListenEvents 批量注册事件监听，对应 ThinkPHP Event.listenEvents。
func (d *Dispatcher) ListenEvents(events map[string][]Listener) error {
	return d.listenEvents(events, false)
}

// ListenApplicationEvents 注册在具体应用解析后加载的监听器。
// 普通事件照常分发，项目级 HttpRun 分发会排除这些监听器。
func (d *Dispatcher) ListenApplicationEvents(events map[string][]Listener) error {
	return d.listenEvents(events, true)
}

func (d *Dispatcher) listenEvents(events map[string][]Listener, application bool) error {
	if d == nil {
		return fmt.Errorf("%w: 调度器不能为空", ErrInvalidListener)
	}
	names := make([]string, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, listener := range events[name] {
			if _, err := d.listen(name, listener, 0, false, application); err != nil {
				return err
			}
		}
	}
	return nil
}

// Listen 注册监听器；first 为 true 时把监听器放到同一事件的首位。
// 对应 ThinkPHP Event.listen 的调用方式。
func (d *Dispatcher) Listen(eventName string, listener Listener, first ...bool) error {
	if len(first) > 1 {
		return fmt.Errorf("%w: 最多只能指定一个首位参数", ErrInvalidListener)
	}
	putFirst := len(first) == 1 && first[0]
	_, err := d.listen(eventName, listener, 0, putFirst, false)
	return err
}

// ListenPriority 是 ThinkGo 对数值优先级的扩展；普通业务应优先使用 Listen。
func (d *Dispatcher) ListenPriority(eventName string, listener Listener, priority int) error {
	_, err := d.listen(eventName, listener, priority, false, false)
	return err
}

// ListenWithHandle 注册监听器并返回独立所有权句柄；first 语义与 Listen 一致。
func (d *Dispatcher) ListenWithHandle(eventName string, listener Listener, first ...bool) (*ListenerHandle, error) {
	if len(first) > 1 {
		return nil, fmt.Errorf("%w: 最多只能指定一个首位参数", ErrInvalidListener)
	}
	putFirst := len(first) == 1 && first[0]
	return d.listen(eventName, listener, 0, putFirst, false)
}

// ListenPriorityWithHandle 注册带优先级的监听器并返回独立所有权句柄。
func (d *Dispatcher) ListenPriorityWithHandle(eventName string, listener Listener, priority int) (*ListenerHandle, error) {
	return d.listen(eventName, listener, priority, false, false)
}

func (d *Dispatcher) listen(eventName string, listener Listener, priority int, first, application bool) (*ListenerHandle, error) {
	if d == nil {
		return nil, fmt.Errorf("%w: 调度器不能为空", ErrInvalidListener)
	}
	d.subscriptionLock.Lock()
	defer d.subscriptionLock.Unlock()
	eventName = d.boundEventName(eventName)
	if err := validateEventName(eventName, true); err != nil {
		return nil, err
	}
	if isNilEventValue(listener) {
		return nil, fmt.Errorf("%w: 监听器不能为空", ErrInvalidListener)
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.listeners == nil {
		d.listeners = make(map[string][]listenerEntry)
	}
	if d.wildcards == nil {
		d.wildcards = make(map[string]struct{})
	}

	d.sequence++
	sequence := d.sequence
	if first {
		priority = int(^uint(0) >> 1)
		sequence = -sequence
	}
	d.listeners[eventName] = append(d.listeners[eventName], listenerEntry{
		listener:    listener,
		priority:    priority,
		sequence:    sequence,
		application: application,
	})
	if containsWildcard(eventName) {
		d.wildcards[eventName] = struct{}{}
	}
	d.invalidateResolutionCacheLocked()
	handle := &ListenerHandle{dispatcher: d, eventName: eventName, sequence: sequence}
	if d.transactionOwner != nil {
		d.transactionHandles = append(d.transactionHandles, handle)
	}
	return handle, nil
}

// Remove 幂等移除该句柄拥有的单个监听器。
func (handle *ListenerHandle) Remove() error {
	if handle == nil {
		return nil
	}
	handle.lock.Lock()
	defer handle.lock.Unlock()
	if handle.removed {
		return handle.err
	}
	handle.removed = true
	if handle.dispatcher == nil {
		handle.err = fmt.Errorf("%w: 监听句柄没有所属调度器", ErrInvalidListener)
		return handle.err
	}
	handle.err = handle.dispatcher.removeListener(handle.eventName, handle.sequence)
	return handle.err
}

func (d *Dispatcher) removeListener(eventName string, sequence int64) error {
	if d == nil {
		return fmt.Errorf("%w: 调度器不能为空", ErrInvalidListener)
	}
	d.subscriptionLock.Lock()
	defer d.subscriptionLock.Unlock()
	d.lock.Lock()
	entries := d.listeners[eventName]
	filtered := entries[:0]
	for _, entry := range entries {
		if entry.sequence != sequence {
			filtered = append(filtered, entry)
		}
	}
	if len(filtered) == 0 {
		delete(d.listeners, eventName)
		delete(d.wildcards, eventName)
	} else {
		d.listeners[eventName] = filtered
	}
	d.invalidateResolutionCacheLocked()
	d.lock.Unlock()
	return nil
}

// Remove 移除指定精确名称或通配模式下的全部监听器，对应 ThinkPHP Event.remove。
func (d *Dispatcher) Remove(eventName string) error {
	if d == nil {
		return fmt.Errorf("%w: 调度器不能为空", ErrInvalidListener)
	}
	d.subscriptionLock.Lock()
	defer d.subscriptionLock.Unlock()
	eventName = d.boundEventName(eventName)
	if err := validateEventName(eventName, true); err != nil {
		return err
	}
	d.lock.Lock()
	delete(d.listeners, eventName)
	delete(d.wildcards, eventName)
	d.invalidateResolutionCacheLocked()
	d.lock.Unlock()
	return nil
}

// Forget 保留原有 ThinkGo 名称，并委托给 ThinkPHP 兼容的 Remove。
func (d *Dispatcher) Forget(eventName string) error {
	return d.Remove(eventName)
}

// RegisterTransaction 在隔离副本中执行一组注册操作，并在全部成功后一次性发布。
// 回调错误或 panic 时，别名、监听器和订阅者产生的全部变更都会被丢弃。
func (d *Dispatcher) RegisterTransaction(register func(*Dispatcher) error) (err error) {
	return d.registerTransaction(register, nil)
}

// RegisterTransactionWithCommitGuard 在扩展回调全部成功后、正式发布前获取提交保护。
// guard 不能执行用户代码；它只应快速检查外部生命周期并返回对应的释放函数。
// 该入口用于应用注册窗口等跨组件边界，避免在用户订阅回调期间持有外部锁。
func (d *Dispatcher) RegisterTransactionWithCommitGuard(
	register func(*Dispatcher) error,
	guard func() (release func(), err error),
) error {
	if guard == nil {
		return fmt.Errorf("%w: 提交保护不能为空", ErrInvalidSubscriber)
	}
	return d.registerTransaction(register, guard)
}

func (d *Dispatcher) registerTransaction(
	register func(*Dispatcher) error,
	guard func() (release func(), err error),
) (err error) {
	if d == nil {
		return fmt.Errorf("%w: 调度器不能为空", ErrInvalidSubscriber)
	}
	if register == nil {
		return fmt.Errorf("%w: 注册事务回调不能为空", ErrInvalidSubscriber)
	}
	d.subscriptionLock.Lock()
	defer d.subscriptionLock.Unlock()
	staged := d.cloneForSubscription()
	var release func()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: RegisterTransaction: %v", ErrEventCallbackPanic, recovered)
		}
	}()
	defer func() {
		if release != nil {
			release()
		}
	}()
	if err = register(staged); err != nil {
		return err
	}
	if guard != nil {
		release, err = guard()
		if err != nil {
			return err
		}
	}
	d.commitSubscription(staged)
	return nil
}

// Subscribe 以独立事务注册订阅者；回调失败或 panic 时只丢弃本次暂存变更。
func (d *Dispatcher) Subscribe(subscriber Subscriber) (err error) {
	return d.subscribe(subscriber, false)
}

// SubscribeApplication 注册在具体应用解析后加载的事件订阅者。
func (d *Dispatcher) SubscribeApplication(subscriber Subscriber) error {
	return d.subscribe(subscriber, true)
}

func (d *Dispatcher) subscribe(subscriber Subscriber, application bool) (err error) {
	if d == nil {
		return fmt.Errorf("%w: 调度器不能为空", ErrInvalidSubscriber)
	}
	if isNilEventValue(subscriber) {
		return fmt.Errorf("%w: 订阅者不能为空", ErrInvalidSubscriber)
	}

	d.subscriptionLock.Lock()
	defer d.subscriptionLock.Unlock()

	staged := d.cloneForSubscription()
	registrationSequence := staged.sequence

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: Subscribe: %v", ErrEventCallbackPanic, recovered)
		}
	}()
	if err = subscriber.Subscribe(staged); err != nil {
		return err
	}
	if application {
		staged.markApplicationListenersAfter(registrationSequence)
	}
	d.commitSubscription(staged)
	return nil
}

func (d *Dispatcher) markApplicationListenersAfter(sequence int64) {
	d.lock.Lock()
	defer d.lock.Unlock()
	for eventName, entries := range d.listeners {
		for index := range entries {
			entrySequence := entries[index].sequence
			if entrySequence < 0 {
				entrySequence = -entrySequence
			}
			if entrySequence > sequence {
				entries[index].application = true
			}
		}
		d.listeners[eventName] = entries
	}
	d.invalidateResolutionCacheLocked()
}

// cloneForSubscription 创建仅供当前订阅回调修改的注册视图。
func (d *Dispatcher) cloneForSubscription() *Dispatcher {
	state := d.captureRegistrationState()
	return &Dispatcher{
		listeners:        state.listeners,
		bindings:         state.bindings,
		wildcards:        state.wildcards,
		resolved:         make(map[string][]listenerEntry),
		sequence:         state.sequence,
		transactionOwner: d,
	}
}

// captureRegistrationState 在锁保护下深拷贝所有可变注册表。
func (d *Dispatcher) captureRegistrationState() dispatcherRegistrationState {
	d.lock.RLock()
	defer d.lock.RUnlock()

	state := dispatcherRegistrationState{
		listeners: make(map[string][]listenerEntry, len(d.listeners)),
		bindings:  make(map[string]eventBinding, len(d.bindings)),
		wildcards: make(map[string]struct{}, len(d.wildcards)),
		sequence:  d.sequence,
	}
	for eventName, entries := range d.listeners {
		state.listeners[eventName] = append([]listenerEntry(nil), entries...)
	}
	for alias, binding := range d.bindings {
		state.bindings[alias] = binding
	}
	for eventName := range d.wildcards {
		state.wildcards[eventName] = struct{}{}
	}
	return state
}

// commitSubscription 原子发布成功订阅的完整注册状态。
func (d *Dispatcher) commitSubscription(staged *Dispatcher) {
	staged.lock.Lock()
	handles := append([]*ListenerHandle(nil), staged.transactionHandles...)
	staged.lock.Unlock()
	// 先锁定全部句柄，再捕获事务状态并发布，使并发 Remove 只能完整发生在
	// 提交之前或之后，避免句柄已经从副本移除但正式状态仍残留监听器。
	for _, handle := range handles {
		handle.lock.Lock()
	}
	defer func() {
		for index := len(handles) - 1; index >= 0; index-- {
			handles[index].lock.Unlock()
		}
	}()

	state := staged.captureRegistrationState()
	d.lock.Lock()
	d.listeners = state.listeners
	d.bindings = state.bindings
	d.wildcards = state.wildcards
	d.sequence = state.sequence
	d.invalidateResolutionCacheLocked()
	for _, handle := range handles {
		handle.dispatcher = d
	}
	if d.transactionOwner != nil {
		d.transactionHandles = append(d.transactionHandles, handles...)
	}
	d.lock.Unlock()
}

// Dispatch 触发事件，支持优先级、通配监听和传播中断。
func (d *Dispatcher) Dispatch(currentEvent Event) error {
	return d.DispatchContext(context.Background(), currentEvent)
}

// Trigger 按事件名称触发事件，对应 ThinkPHP Event.trigger。
func (d *Dispatcher) Trigger(eventName string, data interface{}) error {
	if d == nil {
		return fmt.Errorf("%w: 调度器不能为空", ErrInvalidEvent)
	}
	if err := validateEventName(eventName, false); err != nil {
		return err
	}

	d.lock.RLock()
	binding, bound := d.bindings[eventName]
	d.lock.RUnlock()
	if !bound {
		return d.Dispatch(NewEvent(eventName, data))
	}
	currentEvent, err := safeCreateEvent(binding.factory, data)
	if err != nil {
		return err
	}
	return d.Dispatch(currentEvent)
}

// DispatchContext 使用调用方上下文触发事件；旧 Listener 会保持原有行为。
func (d *Dispatcher) DispatchContext(ctx context.Context, currentEvent Event) error {
	return d.dispatchContext(ctx, currentEvent, true)
}

// DispatchTerminalContext 有界等待必须逐个尝试的终止事件，适用于 HttpEnd 等收尾阶段。
// 调用方超时后，唯一的后台监督任务仍会严格串行执行剩余监听器；当前监听器真实返回前
// 绝不会启动下一项，避免它们重叠访问同一个 Event。StopPropagation 不会终止该分发。
func (d *Dispatcher) DispatchTerminalContext(ctx context.Context, currentEvent Event) error {
	eventName, listeners, err := d.prepareTerminalDispatch(ctx, currentEvent)
	if err != nil {
		return err
	}
	result := make(chan error, 1)
	go func() {
		result <- dispatchTerminalListeners(ctx, eventName, currentEvent, listeners)
	}()
	select {
	case dispatchErr := <-result:
		return dispatchErr
	case <-ctx.Done():
		select {
		case dispatchErr := <-result:
			return dispatchErr
		default:
			return ctx.Err()
		}
	}
}

// DispatchTerminalSerialContext 同步串行分发终止事件。它把 ctx 原样传给上下文监听器，
// 但不会把取消当作跳过剩余监听器的信号；调用方必须在自己的监督任务中使用该原语。
func (d *Dispatcher) DispatchTerminalSerialContext(ctx context.Context, currentEvent Event) error {
	eventName, listeners, err := d.prepareTerminalDispatch(ctx, currentEvent)
	if err != nil {
		return err
	}
	return dispatchTerminalListeners(ctx, eventName, currentEvent, listeners)
}

func (d *Dispatcher) prepareTerminalDispatch(
	ctx context.Context,
	currentEvent Event,
) (string, []listenerEntry, error) {
	if ctx == nil {
		return "", nil, ErrInvalidEventContext
	}
	if d == nil || isNilEventValue(currentEvent) {
		return "", nil, fmt.Errorf("%w: 事件不能为空", ErrInvalidEvent)
	}
	eventName, err := safeEventName(currentEvent)
	if err != nil {
		return "", nil, err
	}
	if err := validateEventName(eventName, false); err != nil {
		return "", nil, err
	}
	return eventName, d.resolveListeners(eventName), nil
}

func dispatchTerminalListeners(
	ctx context.Context,
	eventName string,
	currentEvent Event,
	listeners []listenerEntry,
) error {
	var failures []error
	for index, entry := range listeners {
		if listenerErr := safeHandleContext(ctx, entry.listener, currentEvent); listenerErr != nil {
			failures = append(failures, fmt.Errorf(
				"终止分发事件 %q 的第 %d 个监听器失败: %w",
				eventName,
				index+1,
				listenerErr,
			))
		}
	}
	return errors.Join(failures...)
}

// DispatchProjectContext 只向项目级监听器分发事件，用于 ThinkPHP 多应用
// 在具体应用 event.go 加载之前触发的 HttpRun。
func (d *Dispatcher) DispatchProjectContext(ctx context.Context, currentEvent Event) error {
	return d.dispatchContext(ctx, currentEvent, false)
}

func (d *Dispatcher) dispatchContext(ctx context.Context, currentEvent Event, includeApplication bool) error {
	if ctx == nil {
		return ErrInvalidEventContext
	}
	if d == nil || isNilEventValue(currentEvent) {
		return fmt.Errorf("%w: 事件不能为空", ErrInvalidEvent)
	}
	eventName, err := safeEventName(currentEvent)
	if err != nil {
		return err
	}
	if err := validateEventName(eventName, false); err != nil {
		return err
	}
	listeners := d.resolveListeners(eventName)
	dispatched := 0
	for _, entry := range listeners {
		if !includeApplication && entry.application {
			continue
		}
		dispatched++
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := safeHandleContext(ctx, entry.listener, currentEvent); err != nil {
			return fmt.Errorf("分发事件 %q 的第 %d 个监听器失败: %w", eventName, dispatched, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if stoppable, ok := currentEvent.(interface{ IsPropagationStopped() bool }); ok && stoppable.IsPropagationStopped() {
			return nil
		}
	}
	return nil
}

// HasListener 判断指定事件是否存在精确或通配监听器，对应 ThinkPHP Event.hasListener。
func (d *Dispatcher) HasListener(eventName string) bool {
	if d == nil || validateEventName(eventName, false) != nil {
		return false
	}
	eventName = d.boundEventName(eventName)
	d.lock.RLock()
	if len(d.listeners[eventName]) > 0 {
		d.lock.RUnlock()
		return true
	}
	for pattern := range d.wildcards {
		if len(d.listeners[pattern]) > 0 && wildcardMatch(pattern, eventName) {
			d.lock.RUnlock()
			return true
		}
	}
	d.lock.RUnlock()
	return false
}

// HasListeners 保留原有 ThinkGo 名称，并委托给 ThinkPHP 兼容的 HasListener。
func (d *Dispatcher) HasListeners(eventName string) bool {
	return d.HasListener(eventName)
}

func (d *Dispatcher) resolveListeners(eventName string) []listenerEntry {
	d.lock.RLock()
	epoch := d.resolutionEpoch
	if cached, ok := d.resolved[eventName]; ok {
		listeners := append([]listenerEntry(nil), cached...)
		d.lock.RUnlock()
		return listeners
	}
	collected := append([]listenerEntry(nil), d.listeners[eventName]...)
	for pattern := range d.wildcards {
		if wildcardMatch(pattern, eventName) {
			collected = append(collected, d.listeners[pattern]...)
		}
	}
	d.lock.RUnlock()

	sort.SliceStable(collected, func(i int, j int) bool {
		if collected[i].priority == collected[j].priority {
			return collected[i].sequence < collected[j].sequence
		}
		return collected[i].priority > collected[j].priority
	})

	// 只有监听器集合未发生变化时才写入缓存，避免并发注册造成旧快照污染。
	d.lock.Lock()
	if d.resolutionEpoch == epoch {
		if d.resolved == nil {
			d.resolved = make(map[string][]listenerEntry)
		}
		if len(d.resolved) < maxResolvedEventCacheEntries {
			d.resolved[eventName] = append([]listenerEntry(nil), collected...)
		}
	}
	d.lock.Unlock()
	return collected
}

// invalidateResolutionCacheLocked 使已解析的监听器快照失效；调用方必须持有写锁。
func (d *Dispatcher) invalidateResolutionCacheLocked() {
	d.resolutionEpoch++
	if len(d.resolved) == 0 {
		return
	}
	d.resolved = make(map[string][]listenerEntry)
}

func wildcardMatch(pattern string, eventName string) bool {
	if pattern == "" || pattern == eventName {
		return pattern == eventName
	}
	if !containsWildcard(pattern) {
		return false
	}
	matched, err := path.Match(pattern, eventName)
	return err == nil && matched
}

func containsWildcard(pattern string) bool {
	for _, char := range pattern {
		if char == '*' || char == '?' || char == '[' {
			return true
		}
	}
	return false
}

// SimpleEvent 提供可携带数据、可停止传播的基础事件实现。
type SimpleEvent struct {
	name    string
	Data    interface{}
	stopped atomic.Bool
}

// NewEvent 创建基础事件。
func NewEvent(name string, data interface{}) *SimpleEvent {
	return &SimpleEvent{name: name, Data: data}
}

// Name 返回事件名。
func (e *SimpleEvent) Name() string {
	return e.name
}

// StopPropagation 停止事件继续向后分发。
func (e *SimpleEvent) StopPropagation() {
	e.stopped.Store(true)
}

// IsPropagationStopped 返回事件是否已被终止传播。
func (e *SimpleEvent) IsPropagationStopped() bool {
	return e.stopped.Load()
}

// SimpleListener 允许直接使用函数快速定义监听器。
type SimpleListener struct {
	Handler func(event Event) error
}

// Handle 执行监听器逻辑。
func (l *SimpleListener) Handle(event Event) error {
	if l.Handler != nil {
		return l.Handler(event)
	}
	return nil
}

func prepareEventBinding(alias string, factory Factory) (eventBinding, error) {
	if err := validateEventName(alias, false); err != nil {
		return eventBinding{}, fmt.Errorf("%w: %v", ErrInvalidEventBinding, err)
	}
	if factory == nil {
		return eventBinding{}, fmt.Errorf("%w: %q 的事件工厂不能为空", ErrInvalidEventBinding, alias)
	}
	currentEvent, err := safeCreateEvent(factory, nil)
	if err != nil {
		return eventBinding{}, fmt.Errorf("%w: %q: %w", ErrInvalidEventBinding, alias, err)
	}
	target, err := safeEventName(currentEvent)
	if err != nil {
		return eventBinding{}, fmt.Errorf("%w: %q: %w", ErrInvalidEventBinding, alias, err)
	}
	if err := validateEventName(target, false); err != nil {
		return eventBinding{}, fmt.Errorf("%w: %q 的目标事件无效: %v", ErrInvalidEventBinding, alias, err)
	}
	return eventBinding{factory: factory, target: target}, nil
}

func (d *Dispatcher) boundEventName(eventName string) string {
	if d == nil || containsWildcard(eventName) {
		return eventName
	}
	d.lock.RLock()
	binding, exists := d.bindings[eventName]
	d.lock.RUnlock()
	if exists {
		return binding.target
	}
	return eventName
}

func safeCreateEvent(factory Factory, data interface{}) (currentEvent Event, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: Event.Factory: %v", ErrEventCallbackPanic, recovered)
		}
	}()
	currentEvent = factory(data)
	if isNilEventValue(currentEvent) {
		return nil, fmt.Errorf("%w: 事件工厂返回空事件", ErrInvalidEventBinding)
	}
	return currentEvent, nil
}

func validateEventName(eventName string, allowPattern bool) error {
	if eventName == "" || eventName != strings.TrimSpace(eventName) || len(eventName) > maxEventNameBytes {
		return fmt.Errorf("%w: %q", ErrInvalidEventName, eventName)
	}
	for _, character := range eventName {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: 名称包含控制字符", ErrInvalidEventName)
		}
	}
	if !allowPattern && containsWildcard(eventName) {
		return fmt.Errorf("%w: 分发事件名不能包含通配符", ErrInvalidEventName)
	}
	if allowPattern && containsWildcard(eventName) {
		if _, err := path.Match(eventName, "validation.event"); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidEventName, err)
		}
	}
	return nil
}

func isNilEventValue(value interface{}) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func safeEventName(currentEvent Event) (eventName string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: Event.Name: %v", ErrEventCallbackPanic, recovered)
		}
	}()
	return currentEvent.Name(), nil
}

func safeHandleContext(ctx context.Context, listener Listener, currentEvent Event) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: Listener.HandleContext: %v", ErrEventCallbackPanic, recovered)
		}
	}()
	if contextual, ok := listener.(ContextualListener); ok {
		return contextual.HandleContext(ctx, currentEvent)
	}
	return listener.Handle(currentEvent)
}
