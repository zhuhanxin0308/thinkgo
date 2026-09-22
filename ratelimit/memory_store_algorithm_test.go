package ratelimit

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const latencyBatchSize = 16_384

type referenceEntry struct {
	theoreticalArrival time.Time
	interval           time.Duration
	tolerance          time.Duration
}

type referenceStore struct {
	entries map[string]referenceEntry
}

func (store *referenceStore) take(key string, limit Limit, now time.Time) Result {
	interval, _ := Validate(limit)
	tolerance := time.Duration(limit.Burst-1) * interval
	entry, exists := store.entries[key]
	if !exists || entry.interval != interval || entry.tolerance != tolerance {
		entry = referenceEntry{theoreticalArrival: now, interval: interval, tolerance: tolerance}
	}
	debt := entry.theoreticalArrival.Sub(now)
	if debt > tolerance {
		return Result{
			Allowed:    false,
			Limit:      limit.Burst,
			RetryAfter: debt - tolerance,
			ResetAfter: positiveDuration(debt),
		}
	}
	base := entry.theoreticalArrival
	if now.After(base) {
		base = now
	}
	entry.theoreticalArrival = base.Add(interval)
	store.entries[key] = entry
	newDebt := entry.theoreticalArrival.Sub(now)
	remaining := 0
	if headroom := tolerance - newDebt; headroom >= 0 {
		remaining = int(headroom/interval) + 1
	}
	if remaining > limit.Burst-1 {
		remaining = limit.Burst - 1
	}
	return Result{
		Allowed:    true,
		Limit:      limit.Burst,
		Remaining:  remaining,
		ResetAfter: positiveDuration(newDebt),
	}
}

// TestMemoryStoreMatchesExactReferenceMillionOperations 用一百万次确定性随机操作验证分片实现与简单精确模型完全一致。
func TestMemoryStoreMatchesExactReferenceMillionOperations(t *testing.T) {
	store, err := NewMemoryStore(4096)
	if err != nil {
		t.Fatalf("创建存储失败: %v", err)
	}
	reference := &referenceStore{entries: make(map[string]referenceEntry)}
	limits := []Limit{
		{Rate: 5, Period: time.Second, Burst: 3},
		{Rate: 37, Period: 3 * time.Second, Burst: 11},
		{Rate: 1000, Period: time.Second, Burst: 64},
	}
	keys := make([]string, 1024)
	for index := range keys {
		keys[index] = "client-" + strconv.Itoa(index)
	}
	current := time.Unix(1_000, 0)
	state := uint64(0x9e3779b97f4a7c15)
	for operation := 0; operation < 1_000_000; operation++ {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		current = current.Add(time.Duration(state%2_000_001) * time.Nanosecond)
		key := keys[(state>>11)%uint64(len(keys))]
		limit := limits[(state>>23)%uint64(len(limits))]
		actual, takeErr := store.Take(context.Background(), key, limit, current)
		if takeErr != nil {
			t.Fatalf("第 %d 次操作失败: %v", operation, takeErr)
		}
		expected := reference.take(key, limit, current)
		if actual != expected {
			t.Fatalf("第 %d 次操作差异: key=%s limit=%#v actual=%#v expected=%#v", operation, key, limit, actual, expected)
		}
	}
}

// TestMemoryStoreCapacityPathDoesNotScanEntries 验证满容量请求只读取固定数量的分片提示。
func TestMemoryStoreCapacityPathDoesNotScanEntries(t *testing.T) {
	const entryTotal = 8192
	store, _ := NewMemoryStore(entryTotal)
	limit := Limit{Rate: 1, Period: time.Hour, Burst: 1}
	now := time.Unix(2_000, 0)
	for index := 0; index < entryTotal; index++ {
		if _, err := store.Take(context.Background(), fmt.Sprintf("client-%d", index), limit, now); err != nil {
			t.Fatalf("填充第 %d 个键失败: %v", index, err)
		}
	}
	if _, err := store.Take(context.Background(), "attacker", limit, now); !errors.Is(err, ErrStoreCapacity) {
		t.Fatalf("活跃键占满容量时应拒绝新键，实际为 %v", err)
	}
	work := store.cleanupForCapacity(now)
	if work.hintReads != memoryShardCount || work.heapPops != 0 || work.removed != 0 {
		t.Fatalf("满容量路径应只读取 %d 个提示且不扫描 entry: work=%#v", memoryShardCount, work)
	}
}

// TestMemoryStoreExistingKeyWinsCapacityRace 验证已有键无需再竞争全局容量。
func TestMemoryStoreExistingKeyWinsCapacityRace(t *testing.T) {
	store, _ := NewMemoryStore(1)
	limit := Limit{Rate: 1, Period: time.Second, Burst: 2}
	now := time.Unix(2_500, 0)
	first, err := store.Take(context.Background(), "established", limit, now)
	if err != nil || !first.Allowed || first.Remaining != 1 {
		t.Fatalf("已有键初次判定错误: result=%#v err=%v", first, err)
	}
	if _, err = store.Take(context.Background(), "attacker", limit, now); !errors.Is(err, ErrStoreCapacity) {
		t.Fatalf("新键应在满容量时被拒绝，实际为 %v", err)
	}
	second, err := store.Take(context.Background(), "established", limit, now)
	if err != nil || !second.Allowed || second.Remaining != 0 {
		t.Fatalf("已有键不应受全局 CAS 影响: result=%#v err=%v", second, err)
	}
}

// TestMemoryStoreExpirationRoundsUpAndHandlesLongJump 验证到期 tick 只会晚删，长时间跳跃也不逐 tick 追赶。
func TestMemoryStoreExpirationRoundsUpAndHandlesLongJump(t *testing.T) {
	store, _ := NewMemoryStore(1)
	limit := Limit{Rate: 1, Period: 15 * time.Millisecond, Burst: 1}
	now := time.Unix(3_000, 0)
	if _, err := store.Take(context.Background(), "first", limit, now); err != nil {
		t.Fatalf("初始写入失败: %v", err)
	}
	if _, err := store.Take(context.Background(), "second", limit, now.Add(15*time.Millisecond)); !errors.Is(err, ErrStoreCapacity) {
		t.Fatalf("向上取整 tick 前不应提前删除，实际为 %v", err)
	}
	if _, err := store.Take(context.Background(), "second", limit, now.Add(20*time.Millisecond)); err != nil {
		t.Fatalf("到达向上取整 tick 后应接纳新键: %v", err)
	}

	longStore, _ := NewMemoryStore(64)
	longLimit := Limit{Rate: 1, Period: time.Hour, Burst: 1}
	for index := 0; index < 64; index++ {
		_, _ = longStore.Take(context.Background(), "long-"+strconv.Itoa(index), longLimit, now)
	}
	longJump := time.Duration(200*365*24) * time.Hour
	work := longStore.cleanupForCapacity(now.Add(longJump))
	if work.hintReads != memoryShardCount || work.removed == 0 || work.heapPops > capacityCleanupBudget {
		t.Fatalf("长时间跳跃应在固定预算内回收: work=%#v", work)
	}
	if _, err := longStore.Take(context.Background(), "after-jump", longLimit, now.Add(longJump)); err != nil {
		t.Fatalf("长时间跳跃后应在固定预算内回收容量: %v", err)
	}
}

// TestMemoryStoreRejectsStaleGeneration 验证旧 generation 节点不能删除已续期的 entry。
func TestMemoryStoreRejectsStaleGeneration(t *testing.T) {
	store, _ := NewMemoryStore(1)
	limit := Limit{Rate: 1, Period: time.Second, Burst: 1}
	now := time.Unix(4_000, 0)
	_, _ = store.Take(context.Background(), "stable", limit, now)
	shard := store.shardFor("stable")
	shard.mu.Lock()
	entry := shard.entries["stable"]
	stale := &expirationNode{
		key:         entry.key,
		generation:  entry.generation - 1,
		expiresTick: store.currentTick(now),
	}
	heap.Push(&shard.expirations, stale)
	shard.publishNextExpiration()
	shard.mu.Unlock()

	if _, err := store.Take(context.Background(), "attacker", limit, now); !errors.Is(err, ErrStoreCapacity) {
		t.Fatalf("旧 generation 不应删除活跃键，实际为 %v", err)
	}
	result, err := store.Take(context.Background(), "stable", limit, now)
	if err != nil || result.Allowed {
		t.Fatalf("原 entry 应仍保留限流状态: result=%#v err=%v", result, err)
	}
}

// TestMemoryStoreKeepsOneExpirationNodePerEntry 用 Zipf 热点与广泛键混合轨迹验证到期索引不堆积。
func TestMemoryStoreKeepsOneExpirationNodePerEntry(t *testing.T) {
	store, _ := NewMemoryStore(2048)
	limit := Limit{Rate: 100_000, Period: time.Second, Burst: 100_000}
	now := time.Unix(5_000, 0)
	random := rand.New(rand.NewPCG(7, 11))
	zipf := rand.NewZipf(random, 1.2, 1, 2047)
	for operation := 0; operation < 100_000; operation++ {
		key := "zipf-" + strconv.FormatUint(zipf.Uint64(), 10)
		if _, err := store.Take(context.Background(), key, limit, now); err != nil {
			t.Fatalf("Zipf 轨迹第 %d 次失败: %v", operation, err)
		}
		now = now.Add(time.Microsecond)
	}
	assertExpirationIndexInvariant(t, store)
}

func assertExpirationIndexInvariant(t *testing.T, store *MemoryStore) {
	t.Helper()
	entryTotal := 0
	nodeTotal := 0
	for index := range store.shards {
		shard := &store.shards[index]
		shard.mu.Lock()
		entryTotal += len(shard.entries)
		nodeTotal += len(shard.expirations)
		for key, entry := range shard.entries {
			if entry.expiration == nil || entry.expiration.key != key || entry.expiration.index < 0 ||
				entry.expiration.index >= len(shard.expirations) || shard.expirations[entry.expiration.index] != entry.expiration {
				shard.mu.Unlock()
				t.Fatalf("键 %q 的到期节点索引不一致", key)
			}
		}
		shard.mu.Unlock()
	}
	if nodeTotal != entryTotal || int64(entryTotal) != store.entryCount.Load() {
		t.Fatalf("每个 entry 必须恰好一个节点: entries=%d nodes=%d count=%d", entryTotal, nodeTotal, store.entryCount.Load())
	}
}

// TestMemoryStoreSerializesConcurrentSameKey 验证同一时刻并发消费同键时不会超发额度。
func TestMemoryStoreSerializesConcurrentSameKey(t *testing.T) {
	const (
		burst   = 256
		workers = 2048
	)
	store, _ := NewMemoryStore(16)
	limit := Limit{Rate: 1, Period: time.Second, Burst: burst}
	now := time.Unix(6_000, 0)
	start := make(chan struct{})
	var allowed atomic.Int64
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := store.Take(context.Background(), "shared", limit, now)
			if err != nil {
				errorsFound <- err
				return
			}
			if result.Allowed {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("同键并发判定失败: %v", err)
	}
	if actual := allowed.Load(); actual != burst {
		t.Fatalf("同键并发允许数应为 %d，实际为 %d", burst, actual)
	}
}

// TestMemoryStoreConcurrentTakeResetAndClose 验证关闭与原子判定、重置并发时的终止语义。
func TestMemoryStoreConcurrentTakeResetAndClose(t *testing.T) {
	store, _ := NewMemoryStore(128)
	limit := Limit{Rate: 1000, Period: time.Second, Burst: 1000}
	start := make(chan struct{})
	failures := make(chan error, 64)
	var wait sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wait.Add(1)
		go func(id int) {
			defer wait.Done()
			<-start
			key := "worker-" + strconv.Itoa(id%8)
			for operation := 0; operation < 1000; operation++ {
				_, err := store.Take(context.Background(), key, limit, time.Now())
				if err != nil && !errors.Is(err, ErrStoreClosed) {
					select {
					case failures <- err:
					default:
					}
					return
				}
				if operation%7 == 0 {
					err = store.Reset(context.Background(), key)
					if err != nil && !errors.Is(err, ErrStoreClosed) {
						select {
						case failures <- err:
						default:
						}
						return
					}
				}
				if errors.Is(err, ErrStoreClosed) {
					return
				}
			}
		}(worker)
	}
	close(start)
	if err := store.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("并发操作出现非预期错误: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("重复关闭应幂等: %v", err)
	}
	if store.entryCount.Load() != 0 {
		t.Fatalf("关闭后应释放全部 entry，实际为 %d", store.entryCount.Load())
	}
	if _, err := store.Take(context.Background(), "closed", limit, time.Now()); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("关闭后 Take 应返回 ErrStoreClosed，实际为 %v", err)
	}
	if err := store.Reset(context.Background(), "closed"); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("关闭后 Reset 应返回 ErrStoreClosed，实际为 %v", err)
	}
}

// TestValidateRejectsFullBurstOverflow 验证 Burst*interval 与 (Burst-1)*interval 的全路径溢出边界。
func TestValidateRejectsFullBurstOverflow(t *testing.T) {
	invalid := Limit{Rate: 1, Period: time.Duration(math.MaxInt64), Burst: 2}
	if _, err := Validate(invalid); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("Burst*interval 溢出应被拒绝，实际为 %v", err)
	}
	valid := Limit{Rate: 1, Period: time.Duration(math.MaxInt64 / 2), Burst: 2}
	if _, err := Validate(valid); err != nil {
		t.Fatalf("不溢出的边界应通过，实际为 %v", err)
	}
}

type benchmarkLockedStore struct {
	mu      sync.Mutex
	entries map[string]referenceEntry
}

func (store *benchmarkLockedStore) take(key string, limit Limit, now time.Time) {
	key, _ = normalizeKey(key)
	interval, _ := Validate(limit)
	tolerance := time.Duration(limit.Burst-1) * interval
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, exists := store.entries[key]
	if !exists || entry.interval != interval || entry.tolerance != tolerance {
		entry = referenceEntry{theoreticalArrival: now, interval: interval, tolerance: tolerance}
	}
	if entry.theoreticalArrival.Sub(now) > tolerance {
		return
	}
	base := entry.theoreticalArrival
	if now.After(base) {
		base = now
	}
	entry.theoreticalArrival = base.Add(interval)
	store.entries[key] = entry
}

// BenchmarkMemoryStoreTake32Concurrent 对比分片实现与旧式单锁临界区的 32 并发吞吐和分配。
func BenchmarkMemoryStoreTake32Concurrent(b *testing.B) {
	keys := make([]string, 4096)
	for index := range keys {
		keys[index] = "benchmark-" + strconv.Itoa(index)
	}
	limit := Limit{Rate: maximumRatePerPeriod, Period: time.Second, Burst: maximumBurst}
	b.Run("sharded", func(b *testing.B) {
		store, _ := NewMemoryStore(len(keys))
		for _, key := range keys {
			_, _ = store.Take(context.Background(), key, limit, time.Now())
		}
		b.ReportAllocs()
		b.ResetTimer()
		runBenchmarkWorkers(b.N, 32, func(worker, operation int) {
			index := (worker*997 + operation) & (len(keys) - 1)
			_, _ = store.Take(context.Background(), keys[index], limit, time.Now())
		})
	})
	b.Run("single_lock", func(b *testing.B) {
		store := &benchmarkLockedStore{entries: make(map[string]referenceEntry, len(keys))}
		for _, key := range keys {
			store.take(key, limit, time.Now())
		}
		b.ReportAllocs()
		b.ResetTimer()
		runBenchmarkWorkers(b.N, 32, func(worker, operation int) {
			index := (worker*997 + operation) & (len(keys) - 1)
			store.take(keys[index], limit, time.Now())
		})
	})
}

// BenchmarkMemoryStoreCapacityFlood 采集十万活跃键占满后的新键洪泛成本。
func BenchmarkMemoryStoreCapacityFlood(b *testing.B) {
	const capacity = 100_000
	store, _ := NewMemoryStore(capacity)
	limit := Limit{Rate: 1, Period: time.Hour, Burst: 1}
	now := time.Now()
	for index := 0; index < capacity; index++ {
		_, _ = store.Take(context.Background(), "active-"+strconv.Itoa(index), limit, now)
	}
	attackers := make([]string, 4096)
	for index := range attackers {
		attackers[index] = "attacker-" + strconv.Itoa(index)
	}
	b.ReportAllocs()
	b.ResetTimer()
	runBenchmarkWorkers(b.N, 32, func(worker, operation int) {
		key := attackers[(worker*997+operation)&(len(attackers)-1)]
		_, _ = store.Take(context.Background(), key, limit, now)
	})
}

func runBenchmarkWorkers(total, workers int, operation func(int, int)) {
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		count := total / workers
		if worker < total%workers {
			count++
		}
		wait.Add(1)
		go func(worker, count int) {
			defer wait.Done()
			for current := 0; current < count; current++ {
				operation(worker, current)
			}
		}(worker, count)
	}
	wait.Wait()
}

// TestMemoryStoreLatencyDistribution32Workers 采集分片与单锁实现的 32 并发 p50/p95/p99，不对噪声数据设脆弱阈值。
func TestMemoryStoreLatencyDistribution32Workers(t *testing.T) {
	if testing.Short() {
		t.Skip("短测试跳过延迟分布采集")
	}
	const (
		workers          = 32
		operationsPerRun = 65_536
	)
	keys := make([]string, 4096)
	for index := range keys {
		keys[index] = "latency-" + strconv.Itoa(index)
	}
	limit := Limit{Rate: maximumRatePerPeriod, Period: time.Second, Burst: maximumBurst}
	sharded, _ := NewMemoryStore(len(keys))
	locked := &benchmarkLockedStore{entries: make(map[string]referenceEntry, len(keys))}
	for _, key := range keys {
		now := time.Now()
		_, _ = sharded.Take(context.Background(), key, limit, now)
		locked.take(key, limit, now)
	}
	shardedSamples, shardedElapsed := collectLatencySamples(workers, operationsPerRun, func(key string, now time.Time) {
		_, _ = sharded.Take(context.Background(), key, limit, now)
	}, keys)
	lockedSamples, lockedElapsed := collectLatencySamples(workers, operationsPerRun, func(key string, now time.Time) {
		locked.take(key, limit, now)
	}, keys)
	t.Logf("32 并发分片: throughput=%.0f/s batch-p50/op=%s batch-p95/op=%s batch-p99/op=%s",
		float64(len(shardedSamples)*latencyBatchSize)/shardedElapsed.Seconds(), percentile(shardedSamples, 50), percentile(shardedSamples, 95), percentile(shardedSamples, 99))
	t.Logf("32 并发单锁: throughput=%.0f/s batch-p50/op=%s batch-p95/op=%s batch-p99/op=%s",
		float64(len(lockedSamples)*latencyBatchSize)/lockedElapsed.Seconds(), percentile(lockedSamples, 50), percentile(lockedSamples, 95), percentile(lockedSamples, 99))
}

func collectLatencySamples(
	workers int,
	operations int,
	take func(string, time.Time),
	keys []string,
) ([]time.Duration, time.Duration) {
	const batchSize = latencyBatchSize
	batchesPerWorker := operations / batchSize
	samples := make([]time.Duration, workers*batchesPerWorker)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			<-start
			base := worker * batchesPerWorker
			for batch := 0; batch < batchesPerWorker; batch++ {
				batchStart := time.Now()
				for offset := 0; offset < batchSize; offset++ {
					operation := batch*batchSize + offset
					key := keys[(worker*997+operation)&(len(keys)-1)]
					take(key, time.Now())
				}
				samples[base+batch] = time.Since(batchStart) / batchSize
			}
		}(worker)
	}
	begin := time.Now()
	close(start)
	wait.Wait()
	return samples, time.Since(begin)
}

func percentile(samples []time.Duration, percentage int) time.Duration {
	ordered := append([]time.Duration(nil), samples...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
	index := (len(ordered)*percentage + 99) / 100
	if index <= 0 {
		return ordered[0]
	}
	if index > len(ordered) {
		index = len(ordered)
	}
	return ordered[index-1]
}
