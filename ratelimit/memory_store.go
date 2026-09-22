package ratelimit

import (
	"container/heap"
	"context"
	"fmt"
	"hash/maphash"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// memoryShardCount 在 32 并发基准下兼顾锁冲突与空分片开销，必须保持 2 的整数次幂。
	memoryShardCount = 64
	// memoryExpirationTick 只量化回收时刻，GCRA 判定仍然使用精确 TAT。
	memoryExpirationTick = 10 * time.Millisecond
	// regularCleanupBudget 限制普通请求顺手回收的最大堆节点数。
	regularCleanupBudget = 4
	// capacityCleanupBudget 限制容量满时在请求线程执行的最大堆节点数。
	capacityCleanupBudget = 16
	// capacityTakeAttempts 表示新键在一次预算化回收前后各尝试一次。
	capacityTakeAttempts = 2
)

type memoryEntry struct {
	key                string
	theoreticalArrival time.Time
	interval           time.Duration
	tolerance          time.Duration
	generation         uint64
	expiration         *expirationNode
}

type memoryShard struct {
	mu                 sync.Mutex
	entries            map[string]*memoryEntry
	expirations        expirationHeap
	nextExpirationTick atomic.Uint64
}

// MemoryStore 是全局容量有界的分片精确 GCRA 存储，适用于单实例部署。
//
// 同一键始终由唯一分片锁串行化。每个 entry 只拥有一个可更新堆节点，
// 因此频繁续期不会堆积过期节点。新键通过全局 CAS 获取容量，
// 不会把容量硬分割到各个分片。
type MemoryStore struct {
	shards     [memoryShardCount]memoryShard
	maxEntries int64
	entryCount atomic.Int64
	hashSeed   maphash.Seed
	originOnce sync.Once
	origin     time.Time
	closed     atomic.Bool
	closeOnce  sync.Once
	closeDone  chan struct{}
}

type expirationCleanupWork struct {
	hintReads int
	heapPops  int
	removed   int
}

type expirationNode struct {
	key         string
	generation  uint64
	expiresTick uint64
	index       int
}

type expirationHeap []*expirationNode

func (current expirationHeap) Len() int { return len(current) }

func (current expirationHeap) Less(left, right int) bool {
	if current[left].expiresTick == current[right].expiresTick {
		return current[left].key < current[right].key
	}
	return current[left].expiresTick < current[right].expiresTick
}

func (current expirationHeap) Swap(left, right int) {
	current[left], current[right] = current[right], current[left]
	current[left].index = left
	current[right].index = right
}

func (current *expirationHeap) Push(value interface{}) {
	node := value.(*expirationNode)
	node.index = len(*current)
	*current = append(*current, node)
}

func (current *expirationHeap) Pop() interface{} {
	previous := *current
	lastIndex := len(previous) - 1
	node := previous[lastIndex]
	previous[lastIndex] = nil
	node.index = -1
	*current = previous[:lastIndex]
	return node
}

// NewMemoryStore 创建容量有硬上限的进程内存储。
func NewMemoryStore(maxEntries int) (*MemoryStore, error) {
	if maxEntries <= 0 {
		return nil, fmt.Errorf("%w: maxEntries 必须大于零", ErrInvalidConfiguration)
	}
	return &MemoryStore{
		maxEntries: int64(maxEntries),
		hashSeed:   maphash.MakeSeed(),
		closeDone:  make(chan struct{}),
	}, nil
}

// Take 在键所属分片内原子执行精确 GCRA 判定。
func (store *MemoryStore) Take(ctx context.Context, key string, limit Limit, now time.Time) (Result, error) {
	if store == nil || store.maxEntries <= 0 || ctx == nil {
		return Result{}, ErrInvalidConfiguration
	}
	if store.closed.Load() {
		return Result{}, ErrStoreClosed
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	key, err := normalizeKey(key)
	if err != nil {
		return Result{}, err
	}
	interval, err := Validate(limit)
	if err != nil {
		return Result{}, err
	}
	tolerance := time.Duration(limit.Burst-1) * interval
	store.initializeOrigin(now)
	shard := store.shardFor(key)

	for attempt := 0; attempt < capacityTakeAttempts; attempt++ {
		result, exists, takeErr := store.takeFromShard(ctx, shard, key, limit, interval, tolerance, now)
		if exists || takeErr != nil {
			return result, takeErr
		}
		if attempt > 0 {
			break
		}
		// 只有新键会进入容量回收路径；已有键不受全局容量竞争影响。
		_ = store.cleanupForCapacity(now)
	}
	return Result{}, ErrStoreCapacity
}

func (store *MemoryStore) takeFromShard(
	ctx context.Context,
	shard *memoryShard,
	key string,
	limit Limit,
	interval time.Duration,
	tolerance time.Duration,
	now time.Time,
) (Result, bool, error) {
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if store.closed.Load() {
		return Result{}, true, ErrStoreClosed
	}
	if err := ctx.Err(); err != nil {
		return Result{}, true, err
	}
	entry, exists := shard.entries[key]
	if exists {
		if entry.interval != interval || entry.tolerance != tolerance {
			entry.theoreticalArrival = now
			entry.interval = interval
			entry.tolerance = tolerance
		}
		return store.takeEntryLocked(shard, entry, limit, now), true, nil
	}
	// 先查找再回收，确保活跃已有键不会在容量竞争中被当作新键。
	_, _ = store.cleanupExpiredLocked(shard, now, regularCleanupBudget)
	if !store.reserveEntry() {
		return Result{}, false, nil
	}
	if shard.entries == nil {
		shard.entries = make(map[string]*memoryEntry)
	}
	entry = &memoryEntry{
		key:                key,
		theoreticalArrival: now,
		interval:           interval,
		tolerance:          tolerance,
	}
	shard.entries[key] = entry
	return store.takeEntryLocked(shard, entry, limit, now), true, nil
}

func (store *MemoryStore) takeEntryLocked(shard *memoryShard, entry *memoryEntry, limit Limit, now time.Time) Result {
	debt := entry.theoreticalArrival.Sub(now)
	if debt > entry.tolerance {
		return Result{
			Allowed:    false,
			Limit:      limit.Burst,
			Remaining:  0,
			RetryAfter: debt - entry.tolerance,
			ResetAfter: positiveDuration(debt),
		}
	}
	base := entry.theoreticalArrival
	if now.After(base) {
		base = now
	}
	entry.theoreticalArrival = base.Add(entry.interval)
	entry.generation = nextGeneration(entry.generation)
	store.scheduleExpirationLocked(shard, entry)

	newDebt := entry.theoreticalArrival.Sub(now)
	remaining := 0
	if headroom := entry.tolerance - newDebt; headroom >= 0 {
		remaining = int(headroom/entry.interval) + 1
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

// Reset 原子删除一个键的当前状态；键不存在时也成功。
func (store *MemoryStore) Reset(ctx context.Context, key string) error {
	if store == nil || store.maxEntries <= 0 || ctx == nil {
		return ErrInvalidConfiguration
	}
	if store.closed.Load() {
		return ErrStoreClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := normalizeKey(key)
	if err != nil {
		return err
	}
	shard := store.shardFor(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if store.closed.Load() {
		return ErrStoreClosed
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	entry, exists := shard.entries[key]
	if !exists {
		return nil
	}
	store.removeEntryLocked(shard, entry)
	return nil
}

// Close 幂等关闭存储并释放全部键与到期索引。
func (store *MemoryStore) Close() error {
	if store == nil {
		return nil
	}
	if store.maxEntries <= 0 || store.closeDone == nil {
		return ErrInvalidConfiguration
	}
	store.closeOnce.Do(func() {
		store.closed.Store(true)
		for index := range store.shards {
			shard := &store.shards[index]
			shard.mu.Lock()
			removed := len(shard.entries)
			shard.entries = nil
			for nodeIndex := range shard.expirations {
				shard.expirations[nodeIndex] = nil
			}
			shard.expirations = nil
			shard.nextExpirationTick.Store(0)
			shard.mu.Unlock()
			if removed > 0 {
				store.entryCount.Add(-int64(removed))
			}
		}
		close(store.closeDone)
	})
	<-store.closeDone
	return nil
}

func (store *MemoryStore) initializeOrigin(now time.Time) {
	store.originOnce.Do(func() {
		store.origin = now
	})
}

func (store *MemoryStore) shardFor(key string) *memoryShard {
	var hasher maphash.Hash
	hasher.SetSeed(store.hashSeed)
	_, _ = hasher.WriteString(key)
	return &store.shards[hasher.Sum64()&(memoryShardCount-1)]
}

func (store *MemoryStore) reserveEntry() bool {
	for {
		current := store.entryCount.Load()
		if current >= store.maxEntries {
			return false
		}
		if store.entryCount.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (store *MemoryStore) scheduleExpirationLocked(shard *memoryShard, entry *memoryEntry) {
	expiresTick := store.expirationTick(entry.theoreticalArrival)
	if entry.expiration == nil {
		entry.expiration = &expirationNode{
			key:         entry.key,
			generation:  entry.generation,
			expiresTick: expiresTick,
		}
		heap.Push(&shard.expirations, entry.expiration)
	} else {
		entry.expiration.generation = entry.generation
		// 高频键的 TAT 通常仍落在同一回收 tick，此时堆顺序没有变化。
		if entry.expiration.expiresTick == expiresTick {
			return
		}
		entry.expiration.expiresTick = expiresTick
		heap.Fix(&shard.expirations, entry.expiration.index)
	}
	shard.publishNextExpiration()
}

func (store *MemoryStore) cleanupForCapacity(now time.Time) expirationCleanupWork {
	nowTick := store.currentTick(now)
	selected := -1
	var selectedTick uint64
	work := expirationCleanupWork{}
	for index := range store.shards {
		work.hintReads++
		nextTick := store.shards[index].nextExpirationTick.Load()
		if nextTick == 0 || nextTick > nowTick {
			continue
		}
		if selected < 0 || nextTick < selectedTick {
			selected = index
			selectedTick = nextTick
		}
	}
	if selected < 0 {
		return work
	}
	shard := &store.shards[selected]
	shard.mu.Lock()
	if !store.closed.Load() {
		work.removed, work.heapPops = store.cleanupExpiredLocked(shard, now, capacityCleanupBudget)
	}
	shard.mu.Unlock()
	return work
}

func (store *MemoryStore) cleanupExpiredLocked(shard *memoryShard, now time.Time, budget int) (int, int) {
	nowTick := store.currentTick(now)
	removed := 0
	popped := 0
	for processed := 0; processed < budget && len(shard.expirations) > 0; processed++ {
		node := shard.expirations[0]
		if node.expiresTick > nowTick {
			break
		}
		heap.Pop(&shard.expirations)
		popped++
		entry, exists := shard.entries[node.key]
		if !exists || entry.expiration != node || entry.generation != node.generation {
			continue
		}
		if now.Before(entry.theoreticalArrival) {
			// 理论上只有时钟回退或外部混用不同时间基准才会到达此分支。
			entry.expiration = node
			node.expiresTick = store.expirationTick(entry.theoreticalArrival)
			heap.Push(&shard.expirations, node)
			continue
		}
		delete(shard.entries, node.key)
		entry.expiration = nil
		store.entryCount.Add(-1)
		removed++
	}
	shard.publishNextExpiration()
	return removed, popped
}

func (store *MemoryStore) removeEntryLocked(shard *memoryShard, entry *memoryEntry) {
	delete(shard.entries, entry.key)
	if entry.expiration != nil && entry.expiration.index >= 0 {
		heap.Remove(&shard.expirations, entry.expiration.index)
		entry.expiration = nil
	}
	store.entryCount.Add(-1)
	shard.publishNextExpiration()
}

func (shard *memoryShard) publishNextExpiration() {
	if len(shard.expirations) == 0 {
		shard.nextExpirationTick.Store(0)
		return
	}
	shard.nextExpirationTick.Store(shard.expirations[0].expiresTick)
}

func (store *MemoryStore) expirationTick(deadline time.Time) uint64 {
	delta := deadline.Sub(store.origin)
	if delta <= 0 {
		return 1
	}
	ticks := uint64(delta / memoryExpirationTick)
	if delta%memoryExpirationTick != 0 {
		ticks++
	}
	return ticks + 1
}

func (store *MemoryStore) currentTick(now time.Time) uint64 {
	delta := now.Sub(store.origin)
	if delta < 0 {
		return 0
	}
	return uint64(delta/memoryExpirationTick) + 1
}

func normalizeKey(key string) (string, error) {
	// 先限制原始字节数，避免裁剪后的短子串长期引用攻击者提供的大底层字节。
	if len(key) > maximumKeyBytes {
		return "", ErrInvalidKey
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > maximumKeyBytes || strings.ContainsAny(key, "\r\n\x00") {
		return "", ErrInvalidKey
	}
	return key, nil
}

func nextGeneration(current uint64) uint64 {
	current++
	if current == 0 {
		return 1
	}
	return current
}
