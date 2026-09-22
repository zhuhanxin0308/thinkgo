//go:build integration

package redis

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var liveRedisSequence atomic.Uint64

// liveRedisConfigFromEnv 只读取连接参数，不把密码写入仓库或测试输出。
func liveRedisConfigFromEnv() (map[string]interface{}, bool, error) {
	host, hostOK := os.LookupEnv("THINKGO_LIVE_REDIS_HOST")
	portText, portOK := os.LookupEnv("THINKGO_LIVE_REDIS_PORT")
	if !hostOK || !portOK || host == "" || portText == "" {
		return nil, false, nil
	}
	port, err := strconv.ParseInt(portText, 10, 64)
	if err != nil {
		return nil, true, fmt.Errorf("THINKGO_LIVE_REDIS_PORT 非法: %w", err)
	}
	selectIndex := int64(0)
	if raw, exists := os.LookupEnv("THINKGO_LIVE_REDIS_DB"); exists && raw != "" {
		selectIndex, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, true, fmt.Errorf("THINKGO_LIVE_REDIS_DB 非法: %w", err)
		}
	}
	timeoutMS := int64(1_000)
	if raw, exists := os.LookupEnv("THINKGO_LIVE_REDIS_TIMEOUT_MS"); exists && raw != "" {
		timeoutMS, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, true, fmt.Errorf("THINKGO_LIVE_REDIS_TIMEOUT_MS 非法: %w", err)
		}
	}
	config := map[string]interface{}{
		"host":       host,
		"port":       port,
		"select":     selectIndex,
		"timeout_ms": timeoutMS,
	}
	if password, exists := os.LookupEnv("THINKGO_LIVE_REDIS_PASSWORD"); exists {
		config["password"] = password
	}
	if raw, exists := os.LookupEnv("THINKGO_LIVE_REDIS_TLS"); exists && raw != "" {
		tlsEnabled, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return nil, true, fmt.Errorf("THINKGO_LIVE_REDIS_TLS 非法: %w", parseErr)
		}
		config["tls_enable"] = tlsEnabled
	}
	if raw, exists := os.LookupEnv("THINKGO_LIVE_REDIS_TLS_SERVER_NAME"); exists {
		config["tls_server_name"] = raw
	}
	if raw, exists := os.LookupEnv("THINKGO_LIVE_REDIS_TLS_INSECURE_SKIP_VERIFY"); exists && raw != "" {
		insecure, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return nil, true, fmt.Errorf("THINKGO_LIVE_REDIS_TLS_INSECURE_SKIP_VERIFY 非法: %w", parseErr)
		}
		config["tls_insecure_skip_verify"] = insecure
	}
	return config, true, nil
}

// connectLiveRedis 创建唯一命名空间，并在测试结束时只清理该命名空间。
func connectLiveRedis(tb testing.TB) *Redis {
	tb.Helper()
	config, configured, err := liveRedisConfigFromEnv()
	if err != nil {
		tb.Fatal(err)
	}
	if !configured {
		tb.Skip("THINKGO_LIVE_REDIS_* 未配置")
	}
	config["prefix"] = fmt.Sprintf("thinkgo:live:%d:%d:%d:", os.Getpid(), time.Now().UnixNano(), liveRedisSequence.Add(1))
	driver, err := NewRedis(config)
	if err != nil {
		tb.Fatalf("创建真实 Redis 驱动失败: %v", err)
	}
	tb.Cleanup(func() {
		if clearErr := driver.Clear(); clearErr != nil {
			tb.Errorf("清理真实 Redis 测试前缀失败: %v", clearErr)
		}
		if closeErr := driver.Close(); closeErr != nil {
			tb.Errorf("关闭真实 Redis 驱动失败: %v", closeErr)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := driver.client.Ping(ctx).Err(); err != nil {
		tb.Fatalf("真实 Redis Ping 失败: %v", err)
	}
	return driver
}

// TestLiveRedisOperationContract 验证真实 Redis 的序列化、TTL、原子计数、分布式锁和命名空间清理。
func TestLiveRedisOperationContract(t *testing.T) {
	driver := connectLiveRedis(t)
	if err := driver.Set("value", map[string]interface{}{"name": "Ada", "count": 7}, 0); err != nil {
		t.Fatal(err)
	}
	if err := driver.SetMany(map[string]interface{}{
		"batch-profile": map[string]interface{}{"name": "Grace"},
		"batch-nil":     nil,
	}, time.Minute); err != nil {
		t.Fatalf("真实 Redis 批量写入失败: %v", err)
	}
	batchValues, err := driver.GetMany([]string{"batch-profile", "batch-missing", "batch-nil", "batch-profile"})
	profile, profileOK := batchValues["batch-profile"].(map[string]interface{})
	if err != nil || !profileOK || profile["name"] != "Grace" {
		t.Fatalf("真实 Redis 批量读取失败: values=%#v err=%v", batchValues, err)
	}
	if value, exists := batchValues["batch-nil"]; !exists || value != nil {
		t.Fatalf("真实 Redis 批量 nil 命中错误: value=%#v exists=%t", value, exists)
	}
	value, found, err := driver.Get("value")
	if err != nil || !found {
		t.Fatalf("真实 Redis 读取失败: found=%t err=%v", found, err)
	}
	record, ok := value.(map[string]interface{})
	if !ok || record["name"] != "Ada" || record["count"] != float64(7) {
		t.Fatalf("真实 Redis JSON round-trip 错误: %#v", value)
	}
	if exists, err := driver.Has("value"); err != nil || !exists {
		t.Fatalf("真实 Redis Has 失败: exists=%t err=%v", exists, err)
	}

	if err := driver.Set("ttl", "expires", 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := driver.ctxWithTimeout()
	ttl, err := driver.client.PTTL(ctx, driver.withPrefix("ttl")).Result()
	cancel()
	if err != nil || ttl <= 0 {
		t.Fatalf("真实 Redis TTL 未写入: ttl=%v err=%v", ttl, err)
	}
	expiryDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(expiryDeadline) {
		exists, hasErr := driver.Has("ttl")
		if hasErr != nil {
			t.Fatal(hasErr)
		}
		if !exists {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if exists, err := driver.Has("ttl"); err != nil || exists {
		t.Fatalf("真实 Redis TTL 到期后键仍存在: exists=%t err=%v", exists, err)
	}

	const (
		counterWorkers    = 16
		incrementsPerWork = 50
	)
	var workers sync.WaitGroup
	var firstErr error
	var errorOnce sync.Once
	for worker := 0; worker < counterWorkers; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := 0; index < incrementsPerWork; index++ {
				if _, incrementErr := driver.Inc("counter", 1); incrementErr != nil {
					errorOnce.Do(func() { firstErr = incrementErr })
					return
				}
			}
		}()
	}
	workers.Wait()
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	counter, found, err := driver.Get("counter")
	if err != nil || !found || counter != float64(counterWorkers*incrementsPerWork) {
		t.Fatalf("真实 Redis 原子计数错误: value=%#v found=%t err=%v", counter, found, err)
	}

	lockKey := "lock"
	owner := "owner-a"
	if acquired, err := driver.AcquireLock(lockKey, owner, time.Second); err != nil || !acquired {
		t.Fatalf("真实 Redis 获取锁失败: acquired=%t err=%v", acquired, err)
	}
	t.Cleanup(func() { _, _ = driver.ReleaseLock(lockKey, owner) })
	if acquired, err := driver.AcquireLock(lockKey, "owner-b", time.Second); err != nil || acquired {
		t.Fatalf("真实 Redis 锁未阻止重复 owner: acquired=%t err=%v", acquired, err)
	}
	if renewed, err := driver.RenewLock(lockKey, owner, time.Second); err != nil || !renewed {
		t.Fatalf("真实 Redis 锁续租失败: renewed=%t err=%v", renewed, err)
	}
	if released, err := driver.ReleaseLock(lockKey, "owner-b"); err != nil || released {
		t.Fatalf("真实 Redis 错误 owner 释放了锁: released=%t err=%v", released, err)
	}
	if released, err := driver.ReleaseLock(lockKey, owner); err != nil || !released {
		t.Fatalf("真实 Redis 释放锁失败: released=%t err=%v", released, err)
	}

	if err := driver.Delete("value"); err != nil {
		t.Fatal(err)
	}
	if exists, err := driver.Has("value"); err != nil || exists {
		t.Fatalf("真实 Redis 删除失败: exists=%t err=%v", exists, err)
	}
}

// BenchmarkLiveRedisRoundTrip 测量真实 Redis 在串行和并发场景下的写读往返开销。
func BenchmarkLiveRedisRoundTrip(b *testing.B) {
	driver := connectLiveRedis(b)
	b.Run("sequential", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			key := "bench:" + strconv.Itoa(index%128)
			if err := driver.Set(key, "value", 0); err != nil {
				b.Fatal(err)
			}
			if _, found, err := driver.Get(key); err != nil || !found {
				b.Fatalf("真实 Redis 往返失败: found=%t err=%v", found, err)
			}
		}
	})
	b.Run("parallel-gomaxprocs", func(b *testing.B) {
		b.ReportAllocs()
		var sequence atomic.Uint64
		var failed atomic.Bool
		var firstErr error
		var errorOnce sync.Once
		b.ResetTimer()
		b.RunParallel(func(parallel *testing.PB) {
			for parallel.Next() {
				if failed.Load() {
					return
				}
				key := "bench-parallel:" + strconv.FormatUint(sequence.Add(1)%128, 10)
				if err := driver.Set(key, "value", 0); err != nil {
					errorOnce.Do(func() { firstErr = err })
					failed.Store(true)
					return
				}
				if _, found, err := driver.Get(key); err != nil || !found {
					errorOnce.Do(func() { firstErr = err })
					failed.Store(true)
					return
				}
			}
		})
		if firstErr != nil {
			b.Fatal(firstErr)
		}
	})
}

// BenchmarkLiveRedisBatchRoundTrip 对比真实 Redis 逐键操作和批量 MGET/Pipeline 的往返开销。
func BenchmarkLiveRedisBatchRoundTrip(b *testing.B) {
	driver := connectLiveRedis(b)
	values := make(map[string]interface{}, 32)
	for index := 0; index < 32; index++ {
		values[fmt.Sprintf("batch-%02d", index)] = map[string]interface{}{"index": index}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	b.Run("single-key-32", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			for _, key := range keys {
				if err := driver.Set(key, values[key], time.Minute); err != nil {
					b.Fatal(err)
				}
			}
			for _, key := range keys {
				if _, found, err := driver.Get(key); err != nil || !found {
					b.Fatalf("真实 Redis 单键读取失败: key=%q found=%t err=%v", key, found, err)
				}
			}
		}
	})
	b.Run("batch-32", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			if err := driver.SetMany(values, time.Minute); err != nil {
				b.Fatal(err)
			}
			batchValues, err := driver.GetMany(keys)
			if err != nil || len(batchValues) != len(keys) {
				b.Fatalf("真实 Redis 批量读取失败: count=%d err=%v", len(batchValues), err)
			}
		}
	})
}
