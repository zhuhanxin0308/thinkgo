package event

import (
	"path"
	"sort"
	"sync"
)

// Event 定义事件对象最小接口。
type Event interface {
	Name() string
}

// Listener 定义事件监听器。
type Listener interface {
	Handle(event Event)
}

// Subscriber 允许一个对象批量注册多个监听器。
type Subscriber interface {
	Subscribe(dispatcher *Dispatcher)
}

type listenerEntry struct {
	listener Listener
	priority int
	sequence int64
}

// Dispatcher 负责注册监听器、订阅者以及事件分发。
type Dispatcher struct {
	listeners map[string][]listenerEntry
	lock      sync.RWMutex
	sequence  int64
}

// NewDispatcher 创建事件调度器。
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		listeners: make(map[string][]listenerEntry),
	}
}

// Listen 注册监听器，可选传入优先级，数值越大越先执行。
func (d *Dispatcher) Listen(eventName string, listener Listener, priority ...int) {
	d.lock.Lock()
	defer d.lock.Unlock()

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
}

// Subscribe 注册订阅者。
func (d *Dispatcher) Subscribe(subscriber Subscriber) {
	if subscriber == nil {
		return
	}
	subscriber.Subscribe(d)
}

// Dispatch 触发事件，支持优先级、通配监听和传播中断。
func (d *Dispatcher) Dispatch(event Event) {
	listeners := d.resolveListeners(event.Name())
	for _, entry := range listeners {
		entry.listener.Handle(event)
		if stoppable, ok := event.(interface{ IsPropagationStopped() bool }); ok && stoppable.IsPropagationStopped() {
			return
		}
	}
}

func (d *Dispatcher) resolveListeners(eventName string) []listenerEntry {
	d.lock.RLock()
	defer d.lock.RUnlock()

	collected := make([]listenerEntry, 0)
	for pattern, listeners := range d.listeners {
		if pattern == eventName || wildcardMatch(pattern, eventName) {
			collected = append(collected, listeners...)
		}
	}

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
		if char == '*' || char == '?' {
			return true
		}
	}
	return false
}

// SimpleEvent 提供可携带数据、可停止传播的基础事件实现。
type SimpleEvent struct {
	name    string
	Data    interface{}
	stopped bool
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
	e.stopped = true
}

// IsPropagationStopped 返回事件是否已被终止传播。
func (e *SimpleEvent) IsPropagationStopped() bool {
	return e.stopped
}

// SimpleListener 允许直接使用函数快速定义监听器。
type SimpleListener struct {
	Handler func(event Event)
}

// Handle 执行监听器逻辑。
func (l *SimpleListener) Handle(event Event) {
	if l.Handler != nil {
		l.Handler(event)
	}
}
