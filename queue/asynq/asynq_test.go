package asynq

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	backend "github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/zhuhanxin0308/thinkgo/framework/queue"
)

// TestProducerPersistsOptionsAndRejectsDuplicates 验证负载、Header、重试/超时和唯一性真实写入 Redis。
func TestProducerPersistsOptionsAndRejectsDuplicates(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	producer, err := NewProducerFromRedisClient(client)
	if err != nil {
		t.Fatalf("创建队列生产者失败: %v", err)
	}
	defer producer.Close()
	task, _ := queue.NewTask("mail.send", []byte(`{"id":42}`), map[string]string{"traceparent": "trace-value"})
	options := queue.EnqueueOptions{Queue: "critical", MaxRetry: 3, Timeout: time.Minute, UniqueFor: time.Minute, Retention: time.Hour}
	info, err := producer.Enqueue(context.Background(), task, options)
	if err != nil {
		t.Fatalf("任务入队失败: %v", err)
	}
	if info.ID == "" || info.Queue != "critical" || info.Type != "mail.send" {
		t.Fatalf("入队结果错误: %#v", info)
	}
	inspector := backend.NewInspectorFromRedisClient(client)
	stored, err := inspector.GetTaskInfo(info.Queue, info.ID)
	if err != nil {
		t.Fatalf("读取持久化任务失败: %v", err)
	}
	if string(stored.Payload) != `{"id":42}` || stored.Headers["traceparent"] != "trace-value" || stored.MaxRetry != 3 || stored.Timeout != time.Minute {
		t.Fatalf("持久化任务选项错误: %#v", stored)
	}
	if _, err := producer.Enqueue(context.Background(), task, options); !errors.Is(err, queue.ErrDuplicateTask) {
		t.Fatalf("唯一任务重复入队必须返回稳定错误，实际为 %v", err)
	}
}

// TestWorkerProcessesThroughExactQueueRouter 验证工作进程从 Redis 取任务并经过框架精确路由处理。
func TestWorkerProcessesThroughExactQueueRouter(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	worker, err := NewWorkerFromRedisClient(client, WorkerConfig{
		Concurrency:       1,
		Queues:            map[string]int{"default": 1},
		ShutdownTimeout:   2 * time.Second,
		TaskCheckInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("创建队列工作进程失败: %v", err)
	}
	processed := make(chan queue.Task, 1)
	router := queue.NewRouter()
	if err := router.Register("mail.send", queue.HandlerFunc(func(_ context.Context, task queue.Task) error {
		processed <- task
		return nil
	})); err != nil {
		t.Fatalf("注册任务处理器失败: %v", err)
	}
	if err := worker.Start(router); err != nil {
		t.Fatalf("启动队列工作进程失败: %v", err)
	}
	defer worker.Shutdown()
	producer, _ := NewProducerFromRedisClient(client)
	defer producer.Close()
	task, _ := queue.NewTask("mail.send", []byte("payload"), nil)
	if _, err := producer.Enqueue(context.Background(), task, queue.EnqueueOptions{}); err != nil {
		t.Fatalf("测试任务入队失败: %v", err)
	}
	select {
	case received := <-processed:
		if string(received.Payload()) != "payload" {
			t.Fatalf("工作进程收到错误负载: %q", received.Payload())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("工作进程未在时限内处理任务")
	}
}

// TestConfigurationAndSchedulerLifecycle 验证连接配置、Worker 范围和 Cron 注册生命周期。
func TestConfigurationAndSchedulerLifecycle(t *testing.T) {
	invalidRedis := []RedisConfig{
		{},
		{Address: "redis://localhost:6379"},
		{Address: "localhost:0"},
		{Address: "localhost:6379", DB: -1},
		{Address: "localhost:6379", PoolSize: -1},
		{Address: "localhost:6379", DialTimeout: -time.Second},
		{Address: "localhost:6379", Username: "bad\nuser"},
		{Address: "localhost:6379", TLSConfig: &tls.Config{MinVersion: tls.VersionTLS11}},
	}
	for _, config := range invalidRedis {
		if _, err := config.Options(); !errors.Is(err, queue.ErrBackendUnavailable) {
			t.Fatalf("非法 Redis 配置未被拒绝: %#v err=%v", config, err)
		}
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	if _, err := NewWorkerFromRedisClient(client, WorkerConfig{Concurrency: 0}); err == nil {
		t.Fatal("非法 Worker 并发度必须被拒绝")
	}
	scheduler, err := NewSchedulerFromRedisClient(client, time.UTC)
	if err != nil {
		t.Fatalf("创建调度器失败: %v", err)
	}
	task, _ := queue.NewTask("cleanup.run", nil, nil)
	if _, err := scheduler.Register("invalid cron", task, queue.EnqueueOptions{}); err == nil {
		t.Fatal("非法 Cron 表达式必须被拒绝")
	}
	if id, err := scheduler.Register("@every 1m", task, queue.EnqueueOptions{}); err != nil || id == "" {
		t.Fatalf("注册周期任务失败: id=%q err=%v", id, err)
	}
	if _, err := scheduler.Register("@every 1m", task, queue.EnqueueOptions{ProcessAt: time.Now().Add(time.Minute)}); !errors.Is(err, queue.ErrInvalidOptions) {
		t.Fatalf("周期任务的 ProcessAt 必须被拒绝，实际为 %v", err)
	}
	if err := scheduler.Start(); err != nil {
		t.Fatalf("启动调度器失败: %v", err)
	}
	if _, err := scheduler.Register("@every 1m", task, queue.EnqueueOptions{}); !errors.Is(err, queue.ErrBackendUnavailable) {
		t.Fatalf("调度器启动后注册必须被拒绝，实际为 %v", err)
	}
	if err := scheduler.Start(); !errors.Is(err, queue.ErrBackendUnavailable) {
		t.Fatalf("调度器重复启动必须被拒绝，实际为 %v", err)
	}
	scheduler.Shutdown()
	scheduler.Shutdown()
}

// TestDirectConstructorsAndFailureBoundaries 验证独立连接构造、TLS 防御性复制、取消和非法任务边界。
func TestDirectConstructorsAndFailureBoundaries(t *testing.T) {
	server := miniredis.RunT(t)
	originalTLS := &tls.Config{}
	config := RedisConfig{
		Address:      server.Addr(),
		DialTimeout:  time.Second,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
		PoolSize:     2,
		TLSConfig:    originalTLS,
	}
	options, err := config.Options()
	if err != nil {
		t.Fatalf("合法 Redis 配置校验失败: %v", err)
	}
	if options.TLSConfig == originalTLS || options.TLSConfig.MinVersion != tls.VersionTLS12 || originalTLS.MinVersion != 0 {
		t.Fatal("Redis TLS 配置未防御性复制或未应用 TLS 1.2 下限")
	}

	plainConfig := config
	plainConfig.TLSConfig = nil
	producer, err := NewProducer(plainConfig)
	if err != nil {
		t.Fatalf("创建独立生产者失败: %v", err)
	}
	task, _ := queue.NewTask("direct.task", nil, nil)
	if _, err := producer.Enqueue(context.Background(), task, queue.EnqueueOptions{}); err != nil {
		t.Fatalf("独立生产者入队失败: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := producer.Enqueue(cancelled, task, queue.EnqueueOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("入队必须保留上下文取消错误，实际为 %v", err)
	}
	if _, err := producer.Enqueue(context.Background(), queue.Task{}, queue.EnqueueOptions{}); !errors.Is(err, queue.ErrInvalidTask) {
		t.Fatalf("零值任务必须被拒绝，实际为 %v", err)
	}
	if _, err := producer.Enqueue(context.Background(), task, queue.EnqueueOptions{MaxRetry: -1}); !errors.Is(err, queue.ErrInvalidOptions) {
		t.Fatalf("非法选项必须在访问 Redis 前被拒绝，实际为 %v", err)
	}
	if err := producer.Close(); err != nil {
		t.Fatalf("关闭独立生产者失败: %v", err)
	}
	if err := producer.Close(); err != nil {
		t.Fatalf("重复关闭生产者失败: %v", err)
	}

	worker, err := NewWorker(plainConfig, WorkerConfig{
		Concurrency:       1,
		Queues:            map[string]int{"default": 1},
		ShutdownTimeout:   time.Second,
		TaskCheckInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("创建独立 Worker 失败: %v", err)
	}
	worker.Shutdown()
	scheduler, err := NewScheduler(plainConfig, time.UTC)
	if err != nil {
		t.Fatalf("创建独立 Scheduler 失败: %v", err)
	}
	if _, err := scheduler.Register("@every 1h", task, queue.EnqueueOptions{}); err != nil {
		t.Fatalf("独立 Scheduler 注册失败: %v", err)
	}
	scheduler.Shutdown()
}

// TestWorkerValidationAndRepeatedStart 验证队列权重、关闭边界、类型化 nil 和重复启动。
func TestWorkerValidationAndRepeatedStart(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	invalid := []WorkerConfig{
		{Concurrency: 1, Queues: map[string]int{"default": 1}, ShutdownTimeout: time.Second, TaskCheckInterval: time.Millisecond},
		{Concurrency: 1, Queues: map[string]int{"bad queue": 1}, ShutdownTimeout: time.Second, TaskCheckInterval: time.Second},
		{Concurrency: 1, Queues: map[string]int{"default": 0}, ShutdownTimeout: time.Second, TaskCheckInterval: time.Second},
		{Concurrency: 1, Queues: map[string]int{"default": 1}, ShutdownTimeout: time.Millisecond, TaskCheckInterval: time.Second},
	}
	for _, config := range invalid {
		if _, err := NewWorkerFromRedisClient(client, config); !errors.Is(err, queue.ErrBackendUnavailable) {
			t.Fatalf("非法 Worker 配置未被拒绝: %#v err=%v", config, err)
		}
	}
	valid := WorkerConfig{Concurrency: 1, Queues: map[string]int{"default": 1}, ShutdownTimeout: time.Second, TaskCheckInterval: time.Second}
	worker, err := NewWorkerFromRedisClient(client, valid)
	if err != nil {
		t.Fatalf("创建 Worker 失败: %v", err)
	}
	var nilRouter *queue.Router
	if err := worker.Start(nilRouter); !errors.Is(err, queue.ErrBackendUnavailable) {
		t.Fatalf("类型化 nil Handler 必须被拒绝，实际为 %v", err)
	}
	router := queue.NewRouter()
	_ = router.Register("task", queue.HandlerFunc(func(context.Context, queue.Task) error { return nil }))
	if err := worker.Start(router); err != nil {
		t.Fatalf("启动 Worker 失败: %v", err)
	}
	if err := worker.Start(router); !errors.Is(err, queue.ErrBackendUnavailable) {
		t.Fatalf("重复启动 Worker 必须被拒绝，实际为 %v", err)
	}
	worker.Shutdown()
}

// TestWorkerShutdownBeforeStartClosesLifecycle 验证启动前关闭会形成终态，不能留下可启动但再也无法关闭的 Worker。
func TestWorkerShutdownBeforeStartClosesLifecycle(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	config := WorkerConfig{Concurrency: 1, Queues: map[string]int{"default": 1}, ShutdownTimeout: time.Second, TaskCheckInterval: time.Second}
	worker, err := NewWorkerFromRedisClient(client, config)
	if err != nil {
		t.Fatalf("创建 Worker 失败: %v", err)
	}
	if err := worker.Shutdown(); err != nil {
		t.Fatalf("启动前关闭 Worker 失败: %v", err)
	}
	router := queue.NewRouter()
	_ = router.Register("task", queue.HandlerFunc(func(context.Context, queue.Task) error { return nil }))
	if err := worker.Start(router); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("关闭后的 Worker 必须拒绝启动，实际为 %v", err)
	}
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("Worker 不得关闭借用的 Redis 客户端: %v", err)
	}
}

// TestSchedulerShutdownBeforeStartClosesLifecycle 验证启动前关闭不会消耗一次性回调后继续允许调度器启动。
func TestSchedulerShutdownBeforeStartClosesLifecycle(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	scheduler, err := NewSchedulerFromRedisClient(client, time.UTC)
	if err != nil {
		t.Fatalf("创建 Scheduler 失败: %v", err)
	}
	if err := scheduler.Shutdown(); err != nil {
		t.Fatalf("启动前关闭 Scheduler 失败: %v", err)
	}
	task, _ := queue.NewTask("cleanup.run", nil, nil)
	if _, err := scheduler.Register("@every 1m", task, queue.EnqueueOptions{}); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("关闭后的 Scheduler 必须拒绝注册，实际为 %v", err)
	}
	if err := scheduler.Start(); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("关闭后的 Scheduler 必须拒绝启动，实际为 %v", err)
	}
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("Scheduler 不得关闭借用的 Redis 客户端: %v", err)
	}
}

// TestNilRedisClientsAndSchedulersFailClosed 验证所有复用连接入口都拒绝类型化 nil 和空时区。
func TestNilRedisClientsAndSchedulersFailClosed(t *testing.T) {
	var nilClient *redis.Client
	if _, err := NewProducerFromRedisClient(nilClient); !errors.Is(err, queue.ErrBackendUnavailable) {
		t.Fatalf("生产者必须拒绝类型化 nil Redis，实际为 %v", err)
	}
	validWorker := WorkerConfig{Concurrency: 1, Queues: map[string]int{"default": 1}, ShutdownTimeout: time.Second, TaskCheckInterval: time.Second}
	if _, err := NewWorkerFromRedisClient(nilClient, validWorker); !errors.Is(err, queue.ErrBackendUnavailable) {
		t.Fatalf("Worker 必须拒绝类型化 nil Redis，实际为 %v", err)
	}
	if _, err := NewSchedulerFromRedisClient(nilClient, time.UTC); !errors.Is(err, queue.ErrBackendUnavailable) {
		t.Fatalf("Scheduler 必须拒绝类型化 nil Redis，实际为 %v", err)
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	if _, err := NewSchedulerFromRedisClient(client, nil); !errors.Is(err, queue.ErrBackendUnavailable) {
		t.Fatalf("Scheduler 必须拒绝空时区，实际为 %v", err)
	}
	deploymentLocation := time.FixedZone("deployment-local", 8*60*60)
	if _, err := NewSchedulerFromRedisClient(client, deploymentLocation); !errors.Is(err, queue.ErrInvalidOptions) {
		t.Fatalf("Scheduler 必须拒绝非 UTC 时区，实际为 %v", err)
	}
}
