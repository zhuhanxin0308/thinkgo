package asynq

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	backend "github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/zhuhanxin0308/thinkgo/v3/queue"
)

const (
	maximumWorkerConcurrency = 1024
	maximumWorkerQueues      = 64
	maximumQueuePriority     = 100
	minimumTaskCheckInterval = 50 * time.Millisecond
	maximumTaskCheckInterval = time.Minute
	minimumShutdownTimeout   = time.Second
	maximumShutdownTimeout   = 5 * time.Minute
)

// WorkerConfig 描述工作进程并发度、队列权重和优雅关闭边界。
type WorkerConfig struct {
	Concurrency       int
	Queues            map[string]int
	StrictPriority    bool
	ShutdownTimeout   time.Duration
	TaskCheckInterval time.Duration
}

// Worker 从 Redis 拉取任务并委托给框架 Handler。
type Worker struct {
	mu           sync.Mutex
	server       *backend.Server
	ownedClient  redis.UniversalClient
	state        lifecycleState
	shutdownOnce sync.Once
	shutdownErr  error
}

type lifecycleState uint8

const (
	lifecycleNew lifecycleState = iota
	lifecycleRunning
	lifecycleClosing
	lifecycleClosed
)

// NewWorker 创建拥有独立 Redis 连接池的工作进程。
func NewWorker(redisConfig RedisConfig, workerConfig WorkerConfig) (*Worker, error) {
	options, err := redisOptions(redisConfig)
	if err != nil {
		return nil, err
	}
	backendConfig, err := validateWorkerConfig(workerConfig)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(options)
	return &Worker{server: backend.NewServerFromRedisClient(client, backendConfig), ownedClient: client}, nil
}

// NewWorkerFromRedisClient 复用调用方管理生命周期的 Redis 客户端。
func NewWorkerFromRedisClient(client redis.UniversalClient, config WorkerConfig) (*Worker, error) {
	if err := validateRedisClient(client); err != nil {
		return nil, err
	}
	backendConfig, err := validateWorkerConfig(config)
	if err != nil {
		return nil, err
	}
	return &Worker{server: backend.NewServerFromRedisClient(client, backendConfig)}, nil
}

// Start 启动一次工作进程；重复启动被拒绝。
func (worker *Worker) Start(handler queue.Handler) error {
	if worker == nil || worker.server == nil || isNilQueueHandler(handler) {
		return queue.ErrBackendUnavailable
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	switch worker.state {
	case lifecycleRunning:
		return fmt.Errorf("%w: %w", queue.ErrBackendUnavailable, queue.ErrAlreadyStarted)
	case lifecycleClosing, lifecycleClosed:
		return fmt.Errorf("%w: %w", queue.ErrBackendUnavailable, queue.ErrClosed)
	}
	adapter := backend.HandlerFunc(func(ctx context.Context, task *backend.Task) error {
		if task == nil {
			return queue.ErrInvalidTask
		}
		converted, err := queue.NewTask(task.Type(), task.Payload(), task.Headers())
		if err != nil {
			return err
		}
		return handler.HandleTask(ctx, converted)
	})
	if err := worker.server.Start(adapter); err != nil {
		worker.state = lifecycleClosed
		if worker.ownedClient != nil {
			_ = worker.ownedClient.Close()
			worker.ownedClient = nil
		}
		return fmt.Errorf("%w: 启动 Worker 失败: %v", queue.ErrBackendUnavailable, err)
	}
	worker.state = lifecycleRunning
	return nil
}

// Shutdown 幂等等待进行中的任务完成并停止拉取。
func (worker *Worker) Shutdown() error {
	if worker == nil || worker.server == nil {
		return nil
	}
	// Once 同时提供完成屏障；并发调用不能把“已禁止启动”误认为“已经关闭完成”。
	worker.shutdownOnce.Do(func() {
		worker.mu.Lock()
		previous := worker.state
		worker.state = lifecycleClosing
		ownedClient := worker.ownedClient
		worker.ownedClient = nil
		worker.mu.Unlock()
		if previous == lifecycleRunning {
			worker.server.Shutdown()
		}
		if ownedClient != nil {
			if err := ownedClient.Close(); err != nil {
				worker.shutdownErr = fmt.Errorf("%w: 关闭 Worker Redis 客户端失败: %w", queue.ErrBackendUnavailable, err)
			}
		}
		worker.mu.Lock()
		worker.state = lifecycleClosed
		worker.mu.Unlock()
	})
	return worker.shutdownErr
}

func validateWorkerConfig(config WorkerConfig) (backend.Config, error) {
	if config.Concurrency <= 0 || config.Concurrency > maximumWorkerConcurrency {
		return backend.Config{}, fmt.Errorf("%w: Worker 并发度必须在 1 到 %d 之间", queue.ErrBackendUnavailable, maximumWorkerConcurrency)
	}
	if len(config.Queues) == 0 || len(config.Queues) > maximumWorkerQueues {
		return backend.Config{}, fmt.Errorf("%w: Worker 队列数量必须在 1 到 %d 之间", queue.ErrBackendUnavailable, maximumWorkerQueues)
	}
	queues := make(map[string]int, len(config.Queues))
	for name, priority := range config.Queues {
		options := queue.EnqueueOptions{Queue: name}
		if err := options.Validate(time.Now()); err != nil || priority <= 0 || priority > maximumQueuePriority {
			return backend.Config{}, fmt.Errorf("%w: Worker 队列 %q 或权重非法", queue.ErrBackendUnavailable, name)
		}
		queues[name] = priority
	}
	if config.ShutdownTimeout < minimumShutdownTimeout || config.ShutdownTimeout > maximumShutdownTimeout {
		return backend.Config{}, fmt.Errorf("%w: Worker 关闭时限非法", queue.ErrBackendUnavailable)
	}
	if config.TaskCheckInterval < minimumTaskCheckInterval || config.TaskCheckInterval > maximumTaskCheckInterval {
		return backend.Config{}, fmt.Errorf("%w: Worker 拉取间隔非法", queue.ErrBackendUnavailable)
	}
	return backend.Config{
		Concurrency:       config.Concurrency,
		Queues:            queues,
		StrictPriority:    config.StrictPriority,
		ShutdownTimeout:   config.ShutdownTimeout,
		TaskCheckInterval: config.TaskCheckInterval,
	}, nil
}

func isNilQueueHandler(handler queue.Handler) bool {
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
