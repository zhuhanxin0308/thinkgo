package event

import (
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
)

// Event 定义事件对象最小接口。
type Event interface {
	Name() string
}

// Listener 定义事件监听器。
type Listener interface {
	Handle(event Event) error
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
	listeners map[string][]listenerEntry
	wildcards map[string]struct{}
	lock      sync.RWMutex
	sequence  int64
}

// NewDispatcher 创建事件调度器。
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		listeners: make(map[string][]listenerEntry),
		wildcards: make(map[string]struct{}),
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
	d.lock.Unlock()
	return nil
}

// Subscribe 注册订阅者。
func (d *Dispatcher) Subscribe(subscriber Subscriber) (err error) {
	if d == nil {
		return fmt.Errorf("%w: 调度器不能为空", ErrInvalidSubscriber)
	}
	if isNilEventValue(subscriber) {
		return fmt.Errorf("%w: 订阅者不能为空", ErrInvalidSubscriber)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: Subscribe: %v", ErrEventCallbackPanic, recovered)
		}
	}()
	return subscriber.Subscribe(d)
}

// Dispatch 触发事件，支持优先级、通配监听和传播中断。
func (d *Dispatcher) Dispatch(currentEvent Event) error {
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
		if err := safeHandle(entry.listener, currentEvent); err != nil {
			return fmt.Errorf("分发事件 %q 的第 %d 个监听器失败: %w", eventName, index+1, err)
		}
		if stoppable, ok := currentEvent.(interface{ IsPropagationStopped() bool }); ok && stoppable.IsPropagationStopped() {
			return nil
		}
	}
	return nil
}

func (d *Dispatcher) resolveListeners(eventName string) []listenerEntry {
	d.lock.RLock()
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
	return collected
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

func safeHandle(listener Listener, currentEvent Event) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: Listener.Handle: %v", ErrEventCallbackPanic, recovered)
		}
	}()
	return listener.Handle(currentEvent)
}
