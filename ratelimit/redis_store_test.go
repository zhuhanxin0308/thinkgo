package ratelimit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestRedisStoreSharesAtomicQuota 验证多个实例通过 Redis Lua 原子共享同一额度，
// 并发竞争不会超发突发令牌。
func TestRedisStoreSharesAtomicQuota(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	first, err := NewRedisStore(client, RedisStoreOptions{Prefix: "test:rate:"})
	if err != nil {
		t.Fatalf("创建第一个 Redis 限流存储失败: %v", err)
	}
	second, err := NewRedisStore(client, RedisStoreOptions{Prefix: "test:rate:"})
	if err != nil {
		t.Fatalf("创建第二个 Redis 限流存储失败: %v", err)
	}

	limit := Limit{Rate: 1, Period: 10 * time.Second, Burst: 4}
	const requests = 32
	var allowed atomic.Int64
	var waitGroup sync.WaitGroup
	for index := 0; index < requests; index++ {
		waitGroup.Add(1)
		go func(current int) {
			defer waitGroup.Done()
			store := first
			if current%2 == 1 {
				store = second
			}
			result, takeErr := store.Take(context.Background(), "shared-client", limit, time.Time{})
			if takeErr != nil {
				t.Errorf("并发 Redis 限流失败: %v", takeErr)
				return
			}
			if result.Allowed {
				allowed.Add(1)
			}
		}(index)
	}
	waitGroup.Wait()
	if allowed.Load() != int64(limit.Burst) {
		t.Fatalf("Redis 原子额度发生超发或少发: want=%d got=%d", limit.Burst, allowed.Load())
	}

	denied, err := first.Take(context.Background(), "shared-client", limit, time.Time{})
	if err != nil || denied.Allowed || denied.Limit != limit.Burst || denied.Remaining != 0 || denied.RetryAfter <= 0 || denied.ResetAfter <= 0 {
		t.Fatalf("Redis 拒绝结果不完整: result=%+v err=%v", denied, err)
	}
}

// TestRedisStoreResetAndNamespaceIsolation 验证 Reset 只删除当前命名空间的键，
// 不同前缀可安全复用同一个 Redis 数据库。
func TestRedisStoreResetAndNamespaceIsolation(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	first, _ := NewRedisStore(client, RedisStoreOptions{Prefix: "tenant-a:"})
	second, _ := NewRedisStore(client, RedisStoreOptions{Prefix: "tenant-b:"})
	limit := Limit{Rate: 1, Period: time.Minute, Burst: 1}

	if result, err := first.Take(context.Background(), "client", limit, time.Time{}); err != nil || !result.Allowed {
		t.Fatalf("第一个命名空间首次判定失败: result=%+v err=%v", result, err)
	}
	if result, err := second.Take(context.Background(), "client", limit, time.Time{}); err != nil || !result.Allowed {
		t.Fatalf("第二个命名空间应拥有独立额度: result=%+v err=%v", result, err)
	}
	if err := first.Reset(context.Background(), "client"); err != nil {
		t.Fatalf("重置第一个命名空间失败: %v", err)
	}
	if result, err := first.Take(context.Background(), "client", limit, time.Time{}); err != nil || !result.Allowed {
		t.Fatalf("重置后应恢复额度: result=%+v err=%v", result, err)
	}
	if result, err := second.Take(context.Background(), "client", limit, time.Time{}); err != nil || result.Allowed {
		t.Fatalf("重置不得影响第二个命名空间: result=%+v err=%v", result, err)
	}
}

// TestRedisStoreValidatesBoundaryAndCancellation 验证构造参数、Redis 时间精度和调用上下文均严格失败关闭。
func TestRedisStoreValidatesBoundaryAndCancellation(t *testing.T) {
	var nilClient *redis.Client
	invalidOptions := []RedisStoreOptions{
		{Prefix: "bad\nkey:"},
		{OperationTimeout: -time.Second},
		{OperationTimeout: maximumRedisRateLimitTimeout + time.Second},
	}
	if _, err := NewRedisStore(nilClient, RedisStoreOptions{}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("类型化空 Redis 客户端应被拒绝: %v", err)
	}

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	for _, options := range invalidOptions {
		if _, err := NewRedisStore(client, options); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("非法 Redis 限流选项应被拒绝: options=%+v err=%v", options, err)
		}
	}
	store, err := NewRedisStore(client, RedisStoreOptions{})
	if err != nil {
		t.Fatalf("创建 Redis 限流存储失败: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Take(cancelled, "client", Limit{Rate: 1, Period: time.Second, Burst: 1}, time.Time{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Redis 判定应保留取消原因: %v", err)
	}
	if _, err := store.Take(context.Background(), "client", Limit{Rate: 2_000_000, Period: time.Second, Burst: 1}, time.Time{}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("低于 Redis 微秒精度的间隔应被拒绝: %v", err)
	}
	intervalMicros, toleranceMicros, err := redisRateLimitDurations(time.Duration(1<<63-1), 1)
	if err != nil || intervalMicros <= 0 || toleranceMicros != 0 {
		t.Fatalf("最大 time.Duration 的微秒换算不得溢出: interval=%d tolerance=%d err=%v", intervalMicros, toleranceMicros, err)
	}
}

// TestRedisStoreFailsClosedOnBackendAndStateErrors 验证后端离线、损坏状态、非法键和取消信号
// 都返回明确错误，绝不因为共享存储异常而放行请求。
func TestRedisStoreFailsClosedOnBackendAndStateErrors(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisStore(client, RedisStoreOptions{OperationTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("创建 Redis 限流存储失败: %v", err)
	}
	limit := Limit{Rate: 1, Period: time.Second, Burst: 1}

	if _, err := store.Take(context.Background(), "bad\nkey", limit, time.Time{}); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("非法键应在访问 Redis 前被拒绝: %v", err)
	}
	if err := store.Reset(context.Background(), "bad\x00key"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Reset 非法键应被拒绝: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Reset(cancelled, "client"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reset 应保留取消原因: %v", err)
	}

	server.Set(defaultRedisRateLimitPrefix+"client", "corrupted")
	if _, err := store.Take(context.Background(), "client", limit, time.Time{}); err == nil {
		t.Fatal("损坏的 Redis TAT 必须失败关闭")
	}
	server.Close()
	if _, err := store.Take(context.Background(), "offline", limit, time.Time{}); err == nil {
		t.Fatal("Redis 离线时判定必须失败关闭")
	}
	if err := store.Reset(context.Background(), "offline"); err == nil {
		t.Fatal("Redis 离线时 Reset 必须返回错误")
	}
}
