package asynq

import (
	"fmt"
	"sync"
	"time"

	backend "github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/zhuhanxin0308/thinkgo/v3/queue"
)

const maximumScheduleEntries = 10_000

const schedulerTimezoneName = "UTC"

// Scheduler 使用 Asynq 的 Redis 持久化队列执行 Cron 计划。
type Scheduler struct {
	mu           sync.Mutex
	scheduler    *backend.Scheduler
	ownedClient  redis.UniversalClient
	state        lifecycleState
	entries      int
	shutdownOnce sync.Once
	shutdownErr  error
}

// NewScheduler 创建拥有独立 Redis 连接池的调度器。
func NewScheduler(config RedisConfig, location *time.Location) (*Scheduler, error) {
	options, err := redisOptions(config)
	if err != nil {
		return nil, err
	}
	if err := validateSchedulerLocation(location); err != nil {
		return nil, err
	}
	client := redis.NewClient(options)
	return &Scheduler{
		scheduler:   backend.NewSchedulerFromRedisClient(client, &backend.SchedulerOpts{Location: location}),
		ownedClient: client,
	}, nil
}

// NewSchedulerFromRedisClient 复用调用方管理生命周期的 Redis 客户端。
func NewSchedulerFromRedisClient(client redis.UniversalClient, location *time.Location) (*Scheduler, error) {
	if err := validateRedisClient(client); err != nil {
		return nil, err
	}
	if err := validateSchedulerLocation(location); err != nil {
		return nil, err
	}
	return &Scheduler{scheduler: backend.NewSchedulerFromRedisClient(client, &backend.SchedulerOpts{Location: location})}, nil
}

func validateSchedulerLocation(location *time.Location) error {
	if location == nil {
		return fmt.Errorf("%w: Scheduler 时区不能为空", queue.ErrBackendUnavailable)
	}
	if location.String() != schedulerTimezoneName {
		return fmt.Errorf("%w: Scheduler 时区只能为 UTC", queue.ErrInvalidOptions)
	}
	return nil
}

// Register 在启动前注册周期任务；ProcessAt 与周期计划互斥。
func (scheduler *Scheduler) Register(spec string, task queue.Task, options queue.EnqueueOptions) (string, error) {
	if scheduler == nil || scheduler.scheduler == nil {
		return "", queue.ErrBackendUnavailable
	}
	if !options.ProcessAt.IsZero() {
		return "", fmt.Errorf("%w: 周期任务不能设置 ProcessAt", queue.ErrInvalidOptions)
	}
	converted, err := backendTask(task)
	if err != nil {
		return "", err
	}
	resolvedOptions, err := backendOptions(options, time.Now())
	if err != nil {
		return "", err
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	switch scheduler.state {
	case lifecycleRunning:
		return "", fmt.Errorf("%w: %w", queue.ErrBackendUnavailable, queue.ErrAlreadyStarted)
	case lifecycleClosing, lifecycleClosed:
		return "", fmt.Errorf("%w: %w", queue.ErrBackendUnavailable, queue.ErrClosed)
	}
	if scheduler.entries >= maximumScheduleEntries {
		return "", fmt.Errorf("%w: Scheduler 计划超过 %d 条", queue.ErrBackendUnavailable, maximumScheduleEntries)
	}
	entryID, err := scheduler.scheduler.Register(spec, converted, resolvedOptions...)
	if err != nil {
		return "", fmt.Errorf("%w: Cron 计划非法: %v", queue.ErrInvalidOptions, err)
	}
	scheduler.entries++
	return entryID, nil
}

// Start 启动一次调度器。
func (scheduler *Scheduler) Start() error {
	if scheduler == nil || scheduler.scheduler == nil {
		return queue.ErrBackendUnavailable
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	switch scheduler.state {
	case lifecycleRunning:
		return fmt.Errorf("%w: %w", queue.ErrBackendUnavailable, queue.ErrAlreadyStarted)
	case lifecycleClosing, lifecycleClosed:
		return fmt.Errorf("%w: %w", queue.ErrBackendUnavailable, queue.ErrClosed)
	}
	if err := scheduler.scheduler.Start(); err != nil {
		scheduler.state = lifecycleClosed
		if scheduler.ownedClient != nil {
			_ = scheduler.ownedClient.Close()
			scheduler.ownedClient = nil
		}
		return fmt.Errorf("%w: 启动 Scheduler 失败: %v", queue.ErrBackendUnavailable, err)
	}
	scheduler.state = lifecycleRunning
	return nil
}

// Shutdown 幂等停止计划触发并等待调度器退出。
func (scheduler *Scheduler) Shutdown() error {
	if scheduler == nil || scheduler.scheduler == nil {
		return nil
	}
	// 并发关闭共享同一完成屏障和错误，底层关闭过程不持有注册锁。
	scheduler.shutdownOnce.Do(func() {
		scheduler.mu.Lock()
		previous := scheduler.state
		scheduler.state = lifecycleClosing
		ownedClient := scheduler.ownedClient
		scheduler.ownedClient = nil
		scheduler.mu.Unlock()
		if previous == lifecycleRunning {
			scheduler.scheduler.Shutdown()
		}
		if ownedClient != nil {
			if err := ownedClient.Close(); err != nil {
				scheduler.shutdownErr = fmt.Errorf("%w: 关闭 Scheduler Redis 客户端失败: %w", queue.ErrBackendUnavailable, err)
			}
		}
		scheduler.mu.Lock()
		scheduler.state = lifecycleClosed
		scheduler.mu.Unlock()
	})
	return scheduler.shutdownErr
}
