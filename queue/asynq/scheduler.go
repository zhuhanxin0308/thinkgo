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

type scheduledSignedEntry struct {
	task        queue.Task
	options     queue.EnqueueOptions
	taskIDIndex int
}

// Scheduler 使用 Asynq 的 Redis 持久化队列执行 Cron 计划。
// 若消费端启用了 NewVerifiedWorker，请使用 NewSignedScheduler 或 NewSignedSchedulerFromRedisClient。
type Scheduler struct {
	mu            sync.Mutex
	enqueueMu     sync.Mutex
	scheduler     *backend.Scheduler
	ownedClient   redis.UniversalClient
	state         lifecycleState
	entries       int
	signing       *SigningPolicy
	signedEntries map[*backend.Task]scheduledSignedEntry
	shutdownOnce  sync.Once
	shutdownErr   error
}

// NewScheduler 创建拥有独立 Redis 连接池的无签名调度器。
// 时区只接受标准 time.UTC，名称相同的自定义时区也会被拒绝。
// 若消费端启用了验签工作进程，请改用 NewSignedScheduler。
func NewScheduler(config RedisConfig, location *time.Location) (*Scheduler, error) {
	return newSchedulerWithPolicy(config, location, nil)
}

func newSchedulerWithPolicy(config RedisConfig, location *time.Location, signing *SigningPolicy) (*Scheduler, error) {
	options, err := redisOptions(config)
	if err != nil {
		return nil, err
	}
	if err := validateSchedulerLocation(location); err != nil {
		return nil, err
	}
	client := redis.NewClient(options)
	instance := &Scheduler{
		ownedClient: client,
		signing:     signing,
	}
	instance.scheduler = backend.NewSchedulerFromRedisClient(client, instance.schedulerOptions(location))
	return instance, nil
}

// NewSchedulerFromRedisClient 复用调用方管理生命周期的 Redis 客户端创建无签名调度器。
// 时区只接受标准 time.UTC，time.LoadLocation("UTC") 返回的实例同样有效。
// 若消费端启用了验签工作进程，请改用 NewSignedSchedulerFromRedisClient。
func NewSchedulerFromRedisClient(client redis.UniversalClient, location *time.Location) (*Scheduler, error) {
	return newSchedulerFromRedisClientWithPolicy(client, location, nil)
}

func newSchedulerFromRedisClientWithPolicy(client redis.UniversalClient, location *time.Location, signing *SigningPolicy) (*Scheduler, error) {
	if err := validateRedisClient(client); err != nil {
		return nil, err
	}
	if err := validateSchedulerLocation(location); err != nil {
		return nil, err
	}
	instance := &Scheduler{signing: signing}
	instance.scheduler = backend.NewSchedulerFromRedisClient(client, instance.schedulerOptions(location))
	return instance, nil
}

func (scheduler *Scheduler) schedulerOptions(location *time.Location) *backend.SchedulerOpts {
	opts := &backend.SchedulerOpts{Location: location}
	if scheduler.signing != nil {
		scheduler.signedEntries = make(map[*backend.Task]scheduledSignedEntry)
		opts.PreEnqueueFunc = scheduler.preEnqueueSignedTask
		opts.PostEnqueueFunc = scheduler.postEnqueueSignedTask
	}
	return opts
}

func (scheduler *Scheduler) preEnqueueSignedTask(rawTask *backend.Task, opts []backend.Option) {
	scheduler.enqueueMu.Lock()
	scheduler.mu.Lock()
	entry, ok := scheduler.signedEntries[rawTask]
	signing := scheduler.signing
	scheduler.mu.Unlock()
	if !ok || signing == nil {
		return
	}
	freshSigned, freshID, err := signing.task(entry.task, entry.options, time.Now())
	if err != nil {
		if headers := rawTask.Headers(); headers != nil {
			delete(headers, integrityHeader)
		}
		if entry.taskIDIndex >= 0 && entry.taskIDIndex < len(opts) {
			opts[entry.taskIDIndex] = backend.TaskID("")
		}
		return
	}
	rawTask.Headers()[integrityHeader] = freshSigned.Headers()[integrityHeader]
	if entry.taskIDIndex >= 0 && entry.taskIDIndex < len(opts) {
		opts[entry.taskIDIndex] = backend.TaskID(freshID)
	}
}

func (scheduler *Scheduler) postEnqueueSignedTask(_ *backend.TaskInfo, _ error) {
	scheduler.enqueueMu.Unlock()
}

func validateSchedulerLocation(location *time.Location) error {
	if location == nil {
		return fmt.Errorf("%w: Scheduler 时区不能为空", queue.ErrBackendUnavailable)
	}
	// 名称和单个时刻的偏移均不能证明全年 UTC，必须使用标准实例。
	if location != time.UTC {
		return fmt.Errorf("%w: Scheduler 时区必须使用标准 time.UTC，不接受自定义同名时区", queue.ErrInvalidOptions)
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
	taskIDIndex := -1
	if scheduler.signing != nil {
		var initialID string
		converted, initialID, err = scheduler.signing.task(task, options, time.Now())
		if err != nil {
			return "", err
		}
		taskIDIndex = len(resolvedOptions)
		resolvedOptions = append(resolvedOptions, backend.TaskID(initialID))
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
	if scheduler.signing != nil {
		if scheduler.signedEntries == nil {
			scheduler.signedEntries = make(map[*backend.Task]scheduledSignedEntry)
		}
		scheduler.signedEntries[converted] = scheduledSignedEntry{
			task:        task,
			options:     options,
			taskIDIndex: taskIDIndex,
		}
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
