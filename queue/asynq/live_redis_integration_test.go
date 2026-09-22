//go:build integration

package asynq

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	backend "github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/zhuhanxin0308/thinkgo/framework/queue"
)

const (
	liveQueueWaitTimeout = 2 * time.Minute
	liveQueuePollPeriod  = 50 * time.Millisecond
	liveQueueIOTimeout   = 3 * time.Second
	liveQueueShutdown    = time.Second
	liveQueueRetention   = 5 * time.Minute
)

// TestLiveRedisQueueRetriesFailedTask 验证真实 Redis 持久化负载、传播信息和失败状态，
// 再由后端正式退避调度自动重试；测试不会手工迁移任务或替换重试计时器。
func TestLiveRedisQueueRetriesFailedTask(t *testing.T) {
	live := newLiveQueue(t)
	var attempts atomic.Int32
	live.startWorker(t, queue.HandlerFunc(func(_ context.Context, task queue.Task) error {
		if string(task.Payload()) != "retry-payload" || task.Headers()["traceparent"] != "retry-trace" {
			return errors.New("任务负载或传播信息损坏")
		}
		if attempts.Add(1) == 1 {
			return errors.New("集成测试第一次处理失败")
		}
		return nil
	}))
	info := live.enqueue(t, "retry-payload", "retry-trace")
	retrying := live.waitState(t, info.ID, backend.TaskStateRetry)
	if retrying.Retried != 1 || retrying.MaxRetry != 1 || retrying.LastErr == "" {
		t.Fatalf("真实重试状态未持久化: %#v", retrying)
	}
	completed := live.waitState(t, info.ID, backend.TaskStateCompleted)
	if attempts.Load() != 2 || completed.Retried != 1 || completed.CompletedAt.IsZero() {
		t.Fatalf("任务未按失败后一次重试完成: attempts=%d info=%#v", attempts.Load(), completed)
	}
}

// TestLiveRedisQueueShutdownRestartPreservesTask 验证超出优雅关闭时限的未完成任务
// 会回到 Redis，独立的新 Worker 随后完成原任务，不需要重新入队或补偿写入。
func TestLiveRedisQueueShutdownRestartPreservesTask(t *testing.T) {
	live := newLiveQueue(t)
	started := make(chan struct{}, 1)
	first := live.startWorker(t, queue.HandlerFunc(func(ctx context.Context, _ queue.Task) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}))
	info := live.enqueue(t, "restart-payload", "restart-trace")
	select {
	case <-started:
	case <-time.After(liveQueueWaitTimeout):
		t.Fatal("第一个 Worker 未接收任务")
	}
	active := live.waitState(t, info.ID, backend.TaskStateActive)
	if active.Retried != 0 {
		t.Fatalf("新任务不应提前重试: %#v", active)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- first.Shutdown() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(liveQueueWaitTimeout):
		t.Fatal("Worker 关闭未遵守时限")
	}
	pending := live.waitState(t, info.ID, backend.TaskStatePending)
	if pending.Retried != 0 {
		t.Fatalf("关闭导致的重新排队不应消耗业务重试次数: %#v", pending)
	}
	received := make(chan queue.Task, 1)
	live.startWorker(t, queue.HandlerFunc(func(_ context.Context, task queue.Task) error {
		received <- task
		return nil
	}))
	completed := live.waitState(t, info.ID, backend.TaskStateCompleted)
	select {
	case task := <-received:
		if task.Type() != info.Type || string(task.Payload()) != "restart-payload" || task.Headers()["traceparent"] != "restart-trace" {
			t.Fatalf("重启后的原任务契约丢失: type=%s payload=%q headers=%v", task.Type(), task.Payload(), task.Headers())
		}
	default:
		t.Fatal("任务记录已完成，但新 Worker 没有处理原任务")
	}
	if completed.Retried != 0 || completed.ID != info.ID {
		t.Fatalf("重启恢复必须保留原任务标识和重试预算: %#v", completed)
	}
}

type liveQueue struct {
	config    RedisConfig
	name      string
	producer  *Producer
	inspector *backend.Inspector
}

// newLiveQueue 为每个测试创建不可预测的独立队列，清理仅删除该队列，禁止清空数据库。
func newLiveQueue(t *testing.T) *liveQueue {
	t.Helper()
	host := os.Getenv("THINKGO_LIVE_REDIS_HOST")
	port := os.Getenv("THINKGO_LIVE_REDIS_PORT")
	database := os.Getenv("THINKGO_LIVE_REDIS_DB")
	if host == "" && port == "" && database == "" {
		t.Skip("真实 Redis 集成测试需要 THINKGO_LIVE_REDIS_HOST/PORT/DB")
	}
	if host == "" || port == "" || database == "" {
		t.Fatal("真实 Redis 配置必须完整提供 HOST、PORT 和 DB")
	}
	dbNumber, err := strconv.Atoi(database)
	if err != nil {
		t.Fatal("THINKGO_LIVE_REDIS_DB 必须是整数")
	}
	configuration := RedisConfig{
		Address: net.JoinHostPort(host, port), DB: dbNumber,
		Username: os.Getenv("THINKGO_LIVE_REDIS_USERNAME"), Password: os.Getenv("THINKGO_LIVE_REDIS_PASSWORD"),
		DialTimeout: liveQueueIOTimeout, ReadTimeout: liveQueueIOTimeout, WriteTimeout: liveQueueIOTimeout,
	}
	options, err := redisOptions(configuration)
	if err != nil {
		t.Fatalf("真实 Redis 配置无效: %v", err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("关闭 Redis 检查连接失败: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), liveQueueIOTimeout)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("真实 Redis 无法连接: %v", err)
	}
	producer, err := NewProducer(configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := producer.Close(); err != nil {
			t.Errorf("关闭 Redis 生产者失败: %v", err)
		}
	})
	live := &liveQueue{
		config: configuration, name: "thinkgo-live-" + strings.ToLower(rand.Text()),
		producer: producer, inspector: backend.NewInspectorFromRedisClient(client),
	}
	t.Cleanup(func() {
		if err := live.inspector.DeleteQueue(live.name, true); err != nil && !errors.Is(err, backend.ErrQueueNotFound) {
			t.Errorf("清理本测试队列失败: %v", err)
		}
	})
	return live
}

func (live *liveQueue) startWorker(t *testing.T, handler queue.Handler) *Worker {
	t.Helper()
	worker, err := NewWorker(live.config, WorkerConfig{
		Concurrency: 1, Queues: map[string]int{live.name: 1},
		ShutdownTimeout: liveQueueShutdown, TaskCheckInterval: liveQueuePollPeriod,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := worker.Shutdown(); err != nil {
			t.Errorf("关闭 Redis Worker 失败: %v", err)
		}
	})
	if err := worker.Start(handler); err != nil {
		t.Fatal(err)
	}
	return worker
}

func (live *liveQueue) enqueue(t *testing.T, payload, trace string) queue.Info {
	t.Helper()
	task, err := queue.NewTask("integration.delivery", []byte(payload), map[string]string{"traceparent": trace})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), liveQueueIOTimeout)
	defer cancel()
	info, err := live.producer.Enqueue(ctx, task, queue.EnqueueOptions{
		Queue: live.name, MaxRetry: 1, Timeout: liveQueueWaitTimeout, Retention: liveQueueRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.ID == "" || info.Queue != live.name || info.Type != task.Type() {
		t.Fatalf("持久化入队没有返回正确任务标识: %#v", info)
	}
	return info
}

func (live *liveQueue) waitState(t *testing.T, id string, state backend.TaskState) *backend.TaskInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), liveQueueWaitTimeout)
	defer cancel()
	ticker := time.NewTicker(liveQueuePollPeriod)
	defer ticker.Stop()
	for {
		info, err := live.inspector.GetTaskInfo(live.name, id)
		if err != nil {
			t.Fatalf("读取真实队列任务失败: %v", err)
		}
		if info.State == state {
			return info
		}
		select {
		case <-ctx.Done():
			t.Fatalf("任务未到达 %s: state=%s retried=%d lastErr=%s", state, info.State, info.Retried, info.LastErr)
		case <-ticker.C:
		}
	}
}
