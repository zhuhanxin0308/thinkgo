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
	// ErrEventCallbackPanic 表示事件或回调发生 panic，已被调度器隔离。
	ErrEventCallbackPanic = errors.New("事件回调发生 panic")
	// ErrInvalidEventContext 表示事件分发没有提供有效上下文。
	ErrInvalidEventContext = errors.New("事件分发上下文无效")
)

// Event 定义事件对象最小接口。
type Event interface {
	Name() string
}

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
	listener Listener
	priority int
	sequence int64
}

// Dispatcher 负责注册监听器、订阅者以及事件分发。
type Dispatcher struct {
	listeners        map[string][]listenerEntry
	wildcards        map[string]struct{}
	resolved         map[string][]listenerEntry
	resolutionEpoch  uint64
	subscriptionLock sync.Mutex
	subscriptionTx   *subscriptionTransaction
	lock             sync.RWMutex
	sequence         int64
}

// subscriptionSnapshot 保存一次订阅事务开始前的监听器注册状态。
// sequence 不纳入快照，回滚后继续使用递增序号，避免复用旧序号破坏同优先级监听器的稳定顺序。
type subscriptionSnapshot struct {
	listeners map[string][]listenerEntry
	wildcards map[string]struct{}
}

// subscriptionTransaction 聚合同一注册窗口内的嵌套或并发订阅，避免回滚时相互覆盖。
type subscriptionTransaction struct {
	snapshot subscriptionSnapshot
	active   int
	failed   bool
}

// NewDispatcher 创建事件调度器。
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		listeners: make(map[string][]listenerEntry),
		wildcards: make(map[string]struct{}),
		resolved:  make(map[string][]listenerEntry),
	}
}

// Listen 注册监听器，可选传入一个优先级，数值越大越先执行。
func (d *Dispatcher) Listen(eventName string, listener Listener, priority ...int) error {
	if d == nil {
		return fmt.Errorf("%w: 调度器不能为空", ErrInvalidListener)
	}
	if err := validateEventName(eventName, true); err != nil {
		return err
	}
	if isNilEventValue(listener) {
		return fmt.Errorf("%w: 监听器不能为空", ErrInvalidListener)
	}
	if len(priority) > 1 {
		return fmt.Errorf("%w: 最多只能指定一个优先级", ErrInvalidListener)
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.listeners == nil {
		d.listeners = make(map[string][]listenerEntry)
	}
	if d.wildcards == nil {
		d.wildcards = make(map[string]struct{})
	}

	level := 0
	if len(priority) > 0 {
		level = priority[0]
	}

	d.sequence++
	d.listeners[eventName] = append(d.listeners[eventName], listenerEntry{
		listener: listener,
		priority: level,
		sequence: d.sequence,
	})
	if containsWildcard(eventName) {
		d.wildcards[eventName] = struct{}{}
	}
	d.invalidateResolutionCacheLocked()
	return nil
}

// Forget 移除指定精确名称或通配模式下的全部监听器。
func (d *Dispatcher) Forget(eventName string) error {
	if d == nil {
		return fmt.Errorf("%w: 调度器不能为空", ErrInvalidListener)
	}
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

// Subscribe 以事务方式注册订阅者；当前活动窗口内任一回调失败或 panic 时，窗口内监听器会统一回滚。
func (d *Dispatcher) Subscribe(subscriber Subscriber) (err error) {
	if d == nil {
		return fmt.Errorf("%w: 调度器不能为空", ErrInvalidSubscriber)
	}
	if isNilEventValue(subscriber) {
		return fmt.Errorf("%w: 订阅者不能为空", ErrInvalidSubscriber)
	}

	d.subscriptionLock.Lock()
	if d.subscriptionTx == nil {
		d.subscriptionTx = &subscriptionTransaction{
			snapshot: d.captureSubscriptionSnapshot(),
		}
	}
	tx := d.subscriptionTx
	tx.active++
	d.subscriptionLock.Unlock()

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: Subscribe: %v", ErrEventCallbackPanic, recovered)
		}
		d.finishSubscription(tx, err != nil)
	}()
	return subscriber.Subscribe(d)
}

// finishSubscription 完成一个订阅回调；最后一个回调退出时决定提交还是回滚整个注册窗口。
func (d *Dispatcher) finishSubscription(tx *subscriptionTransaction, failed bool) {
	d.subscriptionLock.Lock()
	if failed {
		tx.failed = true
	}
	tx.active--
	if tx.active > 0 {
		d.subscriptionLock.Unlock()
		return
	}

	shouldRestore := tx.failed
	snapshot := tx.snapshot
	d.subscriptionTx = nil
	if shouldRestore {
		// 持有 subscriptionLock 直到恢复完成，阻止新的订阅在回滚过程中获取过期快照。
		d.restoreSubscriptionSnapshot(snapshot)
	}
	d.subscriptionLock.Unlock()
}

// captureSubscriptionSnapshot 在锁保护下复制监听器注册表，确保失败时可以恢复完整状态。
func (d *Dispatcher) captureSubscriptionSnapshot() subscriptionSnapshot {
	d.lock.RLock()
	defer d.lock.RUnlock()

	snapshot := subscriptionSnapshot{
		listeners: make(map[string][]listenerEntry, len(d.listeners)),
		wildcards: make(map[string]struct{}, len(d.wildcards)),
	}
	for eventName, entries := range d.listeners {
		snapshot.listeners[eventName] = append([]listenerEntry(nil), entries...)
	}
	for eventName := range d.wildcards {
		snapshot.wildcards[eventName] = struct{}{}
	}
	return snapshot
}

// restoreSubscriptionSnapshot 恢复注册表并清空解析缓存，保证回滚后的派发结果只来自已提交监听器。
func (d *Dispatcher) restoreSubscriptionSnapshot(snapshot subscriptionSnapshot) {
	d.lock.Lock()
	defer d.lock.Unlock()

	d.listeners = snapshot.listeners
	d.wildcards = snapshot.wildcards
	d.resolved = make(map[string][]listenerEntry)
	d.resolutionEpoch++
}

// Dispatch 触发事件，支持优先级、通配监听和传播中断。
func (d *Dispatcher) Dispatch(currentEvent Event) error {
	return d.DispatchContext(context.Background(), currentEvent)
}

// DispatchContext 使用调用方上下文触发事件；旧 Listener 会保持原有行为。
func (d *Dispatcher) DispatchContext(ctx context.Context, currentEvent Event) error {
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
	for index, entry := range listeners {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := safeHandleContext(ctx, entry.listener, currentEvent); err != nil {
			return fmt.Errorf("分发事件 %q 的第 %d 个监听器失败: %w", eventName, index+1, err)
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

// HasListeners 判断指定事件是否存在精确或通配监听器，用于 HTTP 生命周期事件的零监听快速路径。
func (d *Dispatcher) HasListeners(eventName string) bool {
	if d == nil || validateEventName(eventName, false) != nil {
		return false
	}
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
