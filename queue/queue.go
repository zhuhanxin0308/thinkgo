// Package queue 定义与具体消息后端解耦的持久化任务契约和精确任务路由。
package queue

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	MaximumTaskTypeBytes    = 128
	MaximumPayloadBytes     = 1 << 20
	MaximumHeaders          = 64
	MaximumHeaderBytes      = 16 << 10
	MaximumRetries          = 100
	DefaultMaxRetries       = 25
	MaximumTaskTimeout      = 24 * time.Hour
	MaximumRetention        = 365 * 24 * time.Hour
	DefaultTaskTimeout      = 30 * time.Minute
	DefaultQueueName        = "default"
	maximumQueueNameBytes   = 64
	maximumHeaderNameBytes  = 128
	maximumHeaderValueBytes = 4096
)

var (
	ErrInvalidTask        = errors.New("队列任务非法")
	ErrInvalidOptions     = errors.New("入队选项非法")
	ErrDuplicateHandler   = errors.New("任务处理器重复")
	ErrHandlerNotFound    = errors.New("任务处理器不存在")
	ErrRouterFrozen       = errors.New("任务路由器已冻结")
	ErrDuplicateTask      = errors.New("队列任务重复")
	ErrBackendUnavailable = errors.New("队列后端不可用")
	ErrAlreadyStarted     = errors.New("队列服务已经启动")
	ErrClosed             = errors.New("队列服务已经关闭")
)

var (
	taskTypePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	queueNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	headerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Task 是在生产者和消费者边界间传递的不可变任务快照。
type Task struct {
	taskType string
	payload  []byte
	headers  map[string]string
}

// NewTask 校验并复制任务类型、负载和传播 Header。
func NewTask(taskType string, payload []byte, headers map[string]string) (Task, error) {
	taskType = strings.TrimSpace(taskType)
	if taskType == "" || len(taskType) > MaximumTaskTypeBytes || !taskTypePattern.MatchString(taskType) {
		return Task{}, fmt.Errorf("%w: 类型 %q", ErrInvalidTask, taskType)
	}
	if len(payload) > MaximumPayloadBytes {
		return Task{}, fmt.Errorf("%w: 负载超过 %d 字节", ErrInvalidTask, MaximumPayloadBytes)
	}
	if len(headers) > MaximumHeaders {
		return Task{}, fmt.Errorf("%w: Header 超过 %d 项", ErrInvalidTask, MaximumHeaders)
	}
	clonedHeaders := make(map[string]string, len(headers))
	totalHeaderBytes := 0
	for name, value := range headers {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > maximumHeaderNameBytes || len(value) > maximumHeaderValueBytes ||
			!headerNamePattern.MatchString(name) || strings.ContainsAny(value, "\r\n\x00") {
			return Task{}, fmt.Errorf("%w: Header %q", ErrInvalidTask, name)
		}
		totalHeaderBytes += len(name) + len(value)
		if totalHeaderBytes > MaximumHeaderBytes {
			return Task{}, fmt.Errorf("%w: Header 总大小超过 %d 字节", ErrInvalidTask, MaximumHeaderBytes)
		}
		if _, exists := clonedHeaders[name]; exists {
			return Task{}, fmt.Errorf("%w: Header %q 重复", ErrInvalidTask, name)
		}
		clonedHeaders[name] = value
	}
	return Task{taskType: taskType, payload: append([]byte(nil), payload...), headers: clonedHeaders}, nil
}

// Type 返回任务类型。
func (task Task) Type() string {
	return task.taskType
}

// Payload 返回负载副本。
func (task Task) Payload() []byte {
	return append([]byte(nil), task.payload...)
}

// Headers 返回传播 Header 副本。
func (task Task) Headers() map[string]string {
	result := make(map[string]string, len(task.headers))
	for name, value := range task.headers {
		result[name] = value
	}
	return result
}

// EnqueueOptions 描述重试、执行时限、计划执行和去重策略。
type EnqueueOptions struct {
	Queue        string
	MaxRetry     int
	DisableRetry bool
	Timeout      time.Duration
	ProcessAt    time.Time
	UniqueFor    time.Duration
	Retention    time.Duration
}

// Validate 校验入队策略；零 Timeout 在适配器中使用安全默认时限。
func (options EnqueueOptions) Validate(now time.Time) error {
	queueName := options.Queue
	if queueName == "" {
		queueName = DefaultQueueName
	}
	if len(queueName) > maximumQueueNameBytes || !queueNamePattern.MatchString(queueName) {
		return fmt.Errorf("%w: 队列名称 %q", ErrInvalidOptions, queueName)
	}
	if options.MaxRetry < 0 || options.MaxRetry > MaximumRetries {
		return fmt.Errorf("%w: MaxRetry 必须在 0 到 %d 之间", ErrInvalidOptions, MaximumRetries)
	}
	if options.DisableRetry && options.MaxRetry != 0 {
		return fmt.Errorf("%w: DisableRetry 不能与 MaxRetry 同时设置", ErrInvalidOptions)
	}
	if options.Timeout < 0 || options.Timeout > MaximumTaskTimeout {
		return fmt.Errorf("%w: Timeout 超出范围", ErrInvalidOptions)
	}
	if !options.ProcessAt.IsZero() && options.ProcessAt.Before(now) {
		return fmt.Errorf("%w: ProcessAt 早于当前时间", ErrInvalidOptions)
	}
	if options.UniqueFor != 0 && (options.UniqueFor < time.Second || options.UniqueFor > MaximumRetention) {
		return fmt.Errorf("%w: UniqueFor 必须至少一秒且不超过一年", ErrInvalidOptions)
	}
	if options.Retention < 0 || options.Retention > MaximumRetention {
		return fmt.Errorf("%w: Retention 超出范围", ErrInvalidOptions)
	}
	return nil
}

// NormalizedQueue 返回空值回退后的队列名。
func (options EnqueueOptions) NormalizedQueue() string {
	if options.Queue == "" {
		return DefaultQueueName
	}
	return options.Queue
}

// EffectiveTimeout 返回零值回退后的安全任务时限。
func (options EnqueueOptions) EffectiveTimeout() time.Duration {
	if options.Timeout == 0 {
		return DefaultTaskTimeout
	}
	return options.Timeout
}

// EffectiveMaxRetry 返回默认重试次数或显式禁用后的零值。
func (options EnqueueOptions) EffectiveMaxRetry() int {
	if options.DisableRetry {
		return 0
	}
	if options.MaxRetry == 0 {
		return DefaultMaxRetries
	}
	return options.MaxRetry
}

// Info 是成功入队后返回的稳定任务标识。
type Info struct {
	ID    string
	Queue string
	Type  string
}

// Producer 定义持久化入队和连接关闭契约。
type Producer interface {
	Enqueue(context.Context, Task, EnqueueOptions) (Info, error)
	Close() error
}

// Handler 处理一个已持久化任务；非 nil 错误由后端重试策略接管。
type Handler interface {
	HandleTask(context.Context, Task) error
}

// HandlerFunc 把函数适配为 Handler。
type HandlerFunc func(context.Context, Task) error

func (handler HandlerFunc) HandleTask(ctx context.Context, task Task) error {
	return handler(ctx, task)
}

// Router 对任务类型进行精确匹配，避免前缀模式意外接管其它业务任务。
type Router struct {
	mu       sync.RWMutex
	handlers map[string]Handler
	frozen   bool
}

// NewRouter 创建处于注册阶段的任务路由器。
func NewRouter() *Router {
	return &Router{handlers: make(map[string]Handler)}
}

// Register 注册任务处理器，重复类型和类型化 nil 都会失败。
func (router *Router) Register(taskType string, handler Handler) error {
	if router == nil || isNilHandler(handler) {
		return ErrInvalidTask
	}
	validated, err := NewTask(taskType, nil, nil)
	if err != nil {
		return err
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.frozen {
		return ErrRouterFrozen
	}
	if _, exists := router.handlers[validated.Type()]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateHandler, validated.Type())
	}
	router.handlers[validated.Type()] = handler
	return nil
}

// Freeze 显式结束注册阶段。
func (router *Router) Freeze() {
	if router == nil {
		return
	}
	router.mu.Lock()
	router.frozen = true
	router.mu.Unlock()
}

// HandleTask 冻结路由器并调用精确类型处理器。
func (router *Router) HandleTask(ctx context.Context, task Task) error {
	if router == nil || ctx == nil {
		return ErrInvalidTask
	}
	validated, err := NewTask(task.Type(), task.Payload(), task.Headers())
	if err != nil {
		return err
	}
	router.mu.Lock()
	router.frozen = true
	handler := router.handlers[validated.Type()]
	router.mu.Unlock()
	if isNilHandler(handler) {
		return fmt.Errorf("%w: %s", ErrHandlerNotFound, validated.Type())
	}
	return handler.HandleTask(ctx, validated)
}

func isNilHandler(handler Handler) bool {
	if handler == nil {
		return true
	}
	value := reflect.ValueOf(handler)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
