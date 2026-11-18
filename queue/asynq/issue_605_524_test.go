package asynq

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	backend "github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/zhuhanxin0308/thinkgo/v3/queue"
)

// TestVerifiedWorkerShutdownRequeueDoesNotArchiveInFlightTask 验证旧实例 ShutdownTimeout 到期重入队后、
// 旧处理器尚未完成 lease.Finish 的竞态窗口内，新实例收到 ErrTaskInProgress 走重试而非 SkipRetry 永久丢弃。
func TestVerifiedWorkerShutdownRequeueDoesNotArchiveInFlightTask(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := &integrityTestStore{}
	signing, verification := integrityPolicies(t, store)

	producer, err := NewSignedProducerFromRedisClient(client, signing)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })

	task, err := queue.NewTask("order.settle", []byte("order-1001"), nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := producer.Enqueue(t.Context(), task, queue.EnqueueOptions{
		MaxRetry:  1,
		Timeout:   5 * time.Second,
		Retention: 500 * time.Millisecond, // 同时验证亚秒 Retention 向上取整至 1s 保留完成状态
	})
	if err != nil {
		t.Fatal(err)
	}

	workerCfg := WorkerConfig{
		Concurrency:       1,
		Queues:            map[string]int{"default": 1},
		ShutdownTimeout:   time.Second,
		TaskCheckInterval: 50 * time.Millisecond,
	}
	firstWorker, err := NewVerifiedWorkerFromRedisClient(client, workerCfg, verification)
	if err != nil {
		t.Fatal(err)
	}

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstExited := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	t.Cleanup(release)

	if err := firstWorker.Start(queue.HandlerFunc(func(_ context.Context, _ queue.Task) error {
		close(firstEntered)
		<-releaseFirst
		return errors.New("interrupted during shutdown")
	})); err != nil {
		t.Fatal(err)
	}

	select {
	case <-firstEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("旧 Worker 未拉取到任务")
	}

	// 触发优雅关闭：1s 后 ShutdownTimeout 到期，Asynq 将任务重推回队列并返回，但旧 handler 仍持有活跃租约。
	if err := firstWorker.Shutdown(); err != nil {
		t.Fatal(err)
	}
	go func() {
		// 保持旧租约跨越 inProgressClaimWaitMax，触发新实例返回可重试的 ErrTaskInProgress。
		time.Sleep(350 * time.Millisecond)
		release()
		close(firstExited)
	}()

	var secondCalls atomic.Int32
	completedCh := make(chan queue.Task, 1)
	secondWorker, err := NewVerifiedWorkerFromRedisClient(client, workerCfg, verification)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondWorker.Shutdown() })
	if err := secondWorker.Start(queue.HandlerFunc(func(_ context.Context, delivered queue.Task) error {
		secondCalls.Add(1)
		completedCh <- delivered
		return nil
	})); err != nil {
		t.Fatal(err)
	}

	select {
	case delivered := <-completedCh:
		if string(delivered.Payload()) != "order-1001" {
			t.Fatalf("重入队任务负载错误: %q", delivered.Payload())
		}
	case <-time.After(5 * time.Second):
		inspector := backend.NewInspectorFromRedisClient(client)
		st, _ := inspector.GetTaskInfo("default", info.ID)
		t.Fatalf("重入队的合法任务未能完成: state=%v lastErr=%q", st, func() string {
			if st != nil {
				return st.LastErr
			}
			return ""
		}())
	}
	<-firstExited

	inspector := backend.NewInspectorFromRedisClient(client)
	stored := waitIntegrityState(t, inspector, info.ID, backend.TaskStateCompleted)
	if stored.Retention != time.Second {
		t.Fatalf("亚秒级 Retention 应向上取整为 1s，实际为 %s", stored.Retention)
	}
	if secondCalls.Load() != 1 {
		t.Fatalf("新实例应恰好成功执行一次，实际为 %d", secondCalls.Load())
	}
}

// TestVerifiedWorkerHandlesPrematureScheduleJitterAndStableTaskIdentity 验证亚秒/秒级时钟偏斜早到任务
// 不会被 SkipRetry 误杀，且不同 ClockSkew 实例间共享一致的原始签名 TaskIdentity.ExpiresAt。
func TestVerifiedWorkerHandlesPrematureScheduleJitterAndStableTaskIdentity(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	key := []byte(strings.Repeat("s", 32))
	store := &integrityTestStore{}
	signing, err := NewSigningPolicy(SigningConfig{
		KeyID: "k1", Key: key, Audience: "orders.prod", ValidFor: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	verifySkewZero, err := NewVerificationPolicy(VerificationConfig{
		Keys: map[string][]byte{"k1": key}, Audience: "orders.prod", MaxValidFor: time.Minute, ClockSkew: 0, Replay: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	verifySkewFiveSec, err := NewVerificationPolicy(VerificationConfig{
		Keys: map[string][]byte{"k1": key}, Audience: "orders.prod", MaxValidFor: time.Minute, ClockSkew: 5 * time.Second, Replay: store,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. 模拟多节点时钟偏斜：生产者时钟比当前消费者快 120ms（且 DisableRetry: true），任务已进入 pending。
	task, err := queue.NewTask("orders.notify", []byte("jitter"), nil)
	if err != nil {
		t.Fatal(err)
	}
	producerNow := time.Now().Add(120 * time.Millisecond)
	wire, id, err := signing.task(task, queue.EnqueueOptions{DisableRetry: true, Timeout: 5 * time.Second}, producerNow)
	if err != nil {
		t.Fatal(err)
	}
	raw := backend.NewClientFromRedisClient(client)
	if _, err := raw.Enqueue(wire, backend.TaskID(id), backend.MaxRetry(0), backend.Timeout(5*time.Second), backend.Retention(time.Minute)); err != nil {
		t.Fatal(err)
	}

	workerCfg := WorkerConfig{
		Concurrency:       1,
		Queues:            map[string]int{"default": 1},
		ShutdownTimeout:   time.Second,
		TaskCheckInterval: 50 * time.Millisecond,
	}
	workerZero, err := NewVerifiedWorkerFromRedisClient(client, workerCfg, verifySkewZero)
	if err != nil {
		t.Fatal(err)
	}
	doneJitter := make(chan struct{}, 1)
	if err := workerZero.Start(queue.HandlerFunc(func(_ context.Context, _ queue.Task) error {
		doneJitter <- struct{}{}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-doneJitter:
	case <-time.After(3 * time.Second):
		_ = workerZero.Shutdown()
		t.Fatal("零默认 ClockSkew 下轻微早到的合法任务被误丢弃")
	}
	if err := workerZero.Shutdown(); err != nil {
		t.Fatal(err)
	}

	// 2. 验证滚动更新调整 ClockSkew 时，跨 Worker 重试不会因 TaskIdentity.ExpiresAt 混入本地 ClockSkew 而冲突。
	retryWire, retryID, err := signing.task(task, queue.EnqueueOptions{MaxRetry: 1, Timeout: 5 * time.Second}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var env signedEnvelope
	if err := json.Unmarshal([]byte(retryWire.Headers()[integrityHeader]), &env); err != nil {
		t.Fatal(err)
	}
	inspector := backend.NewInspectorFromRedisClient(client)

	w1, err := NewVerifiedWorkerFromRedisClient(client, workerCfg, verifySkewZero)
	if err != nil {
		t.Fatal(err)
	}
	if err := w1.Start(queue.HandlerFunc(func(context.Context, queue.Task) error {
		return errors.New("transient error on old clock-skew worker")
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Enqueue(retryWire, backend.TaskID(retryID), backend.MaxRetry(1), backend.Timeout(5*time.Second), backend.Retention(time.Minute)); err != nil {
		t.Fatal(err)
	}
	_ = waitIntegrityState(t, inspector, retryID, backend.TaskStateRetry)
	if err := w1.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if err := inspector.DeleteTask("default", retryID); err != nil {
		t.Fatal(err)
	}

	w2, err := NewVerifiedWorkerFromRedisClient(client, workerCfg, verifySkewFiveSec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w2.Shutdown() })
	retriedOK := make(chan struct{}, 1)
	if err := w2.Start(queue.HandlerFunc(func(context.Context, queue.Task) error {
		retriedOK <- struct{}{}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Enqueue(retryWire, backend.TaskID(retryID), backend.MaxRetry(1), backend.Timeout(5*time.Second), backend.Retention(time.Minute)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-retriedOK:
	case <-time.After(3 * time.Second):
		t.Fatal("跨不同 ClockSkew 实例重试被误判为身份冲突")
	}
	_ = waitIntegrityState(t, inspector, retryID, backend.TaskStateCompleted)

	store.mu.Lock()
	rec := store.records[retryID]
	gotExpires := rec.identity.ExpiresAt
	store.mu.Unlock()
	if !gotExpires.Equal(time.Unix(0, env.Metadata.Expires)) {
		t.Fatalf("TaskIdentity.ExpiresAt 混入了本地 ClockSkew: got=%v want=%v", gotExpires, time.Unix(0, env.Metadata.Expires))
	}
}

// TestProducerRoundsSubSecondRetentionAndUniqueForUpward 验证非零亚秒/小数秒 Retention 与 UniqueFor 向上取整至整秒。
func TestProducerRoundsSubSecondRetentionAndUniqueForUpward(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	producer, err := NewProducerFromRedisClient(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })

	task, err := queue.NewTask("audit.log", []byte("entry"), nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := producer.Enqueue(t.Context(), task, queue.EnqueueOptions{
		Retention: 500 * time.Millisecond,
		UniqueFor: 1500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	inspector := backend.NewInspectorFromRedisClient(client)
	stored, err := inspector.GetTaskInfo(info.Queue, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Retention != time.Second {
		t.Fatalf("500ms Retention 被向零截断: got=%s want=1s", stored.Retention)
	}
	// 验证 1500ms UniqueFor 向上取整为 2s（在 1.1s 时仍保持去重）。
	server.FastForward(1100 * time.Millisecond)
	if _, err := producer.Enqueue(t.Context(), task, queue.EnqueueOptions{UniqueFor: 1500 * time.Millisecond}); !errors.Is(err, queue.ErrDuplicateTask) {
		t.Fatalf("1500ms UniqueFor 未按 2s 向上取整保持唯一锁: %v", err)
	}
}

// TestSignedSchedulerSignsEveryCronTriggerForVerifiedWorker 验证签名调度器每次 Cron 触发都生成全新签名与 ID，
// 并能被启用验签的 VerifiedWorker 连续多次正常消费执行。
func TestSignedSchedulerSignsEveryCronTriggerForVerifiedWorker(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := &integrityTestStore{}
	key := []byte(strings.Repeat("c", 32))
	signing, err := NewSigningPolicy(SigningConfig{
		KeyID: "cron-k1", Key: key, Audience: "cron.prod", ValidFor: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	verification, err := NewVerificationPolicy(VerificationConfig{
		Keys: map[string][]byte{"cron-k1": key}, Audience: "cron.prod", MaxValidFor: 5 * time.Second, Replay: store,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 校验构造参数与注册期失败闭环。
	for _, badPolicy := range []*SigningPolicy{nil, {}} {
		if _, err := NewSignedScheduler(RedisConfig{Address: server.Addr()}, time.UTC, badPolicy); !errors.Is(err, queue.ErrInvalidOptions) {
			t.Fatalf("NewSignedScheduler 未拒绝非法签名策略: %v", err)
		}
		if _, err := NewSignedSchedulerFromRedisClient(client, time.UTC, badPolicy); !errors.Is(err, queue.ErrInvalidOptions) {
			t.Fatalf("NewSignedSchedulerFromRedisClient 未拒绝非法签名策略: %v", err)
		}
	}
	if _, err := NewSignedScheduler(RedisConfig{}, time.UTC, signing); !errors.Is(err, queue.ErrBackendUnavailable) {
		t.Fatalf("NewSignedScheduler 未拒绝非法 Redis 配置: %v", err)
	}
	if _, err := NewSignedSchedulerFromRedisClient(nil, time.UTC, signing); !errors.Is(err, queue.ErrBackendUnavailable) {
		t.Fatalf("NewSignedSchedulerFromRedisClient 未拒绝空客户端: %v", err)
	}
	if _, err := NewSignedScheduler(RedisConfig{Address: server.Addr()}, time.FixedZone("UTC", 0), signing); !errors.Is(err, queue.ErrInvalidOptions) {
		t.Fatalf("NewSignedScheduler 未拒绝伪造 UTC 时区: %v", err)
	}

	ownedSched, err := NewSignedScheduler(RedisConfig{Address: server.Addr()}, time.UTC, signing)
	if err != nil {
		t.Fatal(err)
	}
	if err := ownedSched.Shutdown(); err != nil {
		t.Fatal(err)
	}

	sched, err := NewSignedSchedulerFromRedisClient(client, time.UTC, signing)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sched.Shutdown() })

	cronTask, err := queue.NewTask("billing.reconcile", []byte("cron-payload"), map[string]string{"source": "cron"})
	if err != nil {
		t.Fatal(err)
	}
	// Timeout >= ValidFor 应在 Register 阶段即快速失败，不留到运行期静默丢弃。
	if _, err := sched.Register("@every 1s", cronTask, queue.EnqueueOptions{Timeout: 5 * time.Second}); !errors.Is(err, queue.ErrInvalidOptions) {
		t.Fatalf("超出签名有效期的周期任务应在注册期拒绝: %v", err)
	}

	if _, err := sched.Register("@every 1s", cronTask, queue.EnqueueOptions{
		DisableRetry: true,
		Timeout:      2 * time.Second,
		Retention:    time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	worker, err := NewVerifiedWorkerFromRedisClient(client, WorkerConfig{
		Concurrency:       1,
		Queues:            map[string]int{"default": 1},
		ShutdownTimeout:   time.Second,
		TaskCheckInterval: 50 * time.Millisecond,
	}, verification)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Shutdown() })

	type executionRecord struct {
		id      string
		headers map[string]string
		payload string
	}
	executed := make(chan executionRecord, 4)
	if err := worker.Start(queue.HandlerFunc(func(ctx context.Context, delivered queue.Task) error {
		id, _ := backend.GetTaskID(ctx)
		executed <- executionRecord{
			id:      id,
			headers: delivered.Headers(),
			payload: string(delivered.Payload()),
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := sched.Start(); err != nil {
		t.Fatal(err)
	}

	var seenIDs []string
	timeout := time.After(4500 * time.Millisecond)
	for len(seenIDs) < 2 {
		select {
		case rec := <-executed:
			if rec.payload != "cron-payload" || rec.headers["source"] != "cron" || len(rec.headers) != 1 {
				t.Fatalf("周期签名任务负载或头部异常: %+v", rec)
			}
			if len(rec.id) != 64 {
				t.Fatalf("周期签名任务 ID 长度异常: %q", rec.id)
			}
			seenIDs = append(seenIDs, rec.id)
		case <-timeout:
			t.Fatalf("VerifiedWorker 未能连续消费 2 次周期签名任务，已执行次数=%d", len(seenIDs))
		}
	}
	if seenIDs[0] == seenIDs[1] {
		t.Fatalf("连续两次 Cron 触发复用了同一签名 ID: %q", seenIDs[0])
	}
}
