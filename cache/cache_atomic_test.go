package cache

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver"
)

type postCommitInvalidatingDriver struct {
	*cacheDriver.Memory
	target          string
	invalidationKey string
	once            sync.Once
}

func (*postCommitInvalidatingDriver) supportsTagMutationLock() bool { return false }

func (driver *postCommitInvalidatingDriver) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	if err := driver.Memory.Update(key, ttl, update); err != nil {
		return err
	}
	if key == driver.target {
		driver.once.Do(func() {
			_ = driver.Memory.Set(driver.invalidationKey, "999", 0)
		})
	}
	return nil
}

type failingFencedBatchDriver struct {
	*cacheDriver.Memory
	failKey string
	err     error
}

type blockingIndependentUpdateDriver struct {
	*cacheDriver.Memory
	target  string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (driver *blockingIndependentUpdateDriver) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	if key == driver.target {
		driver.once.Do(func() { close(driver.started) })
		<-driver.release
	}
	return driver.Memory.Update(key, ttl, update)
}

func (*failingFencedBatchDriver) supportsTagMutationLock() bool { return false }

func (driver *failingFencedBatchDriver) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	if key == driver.failKey {
		return driver.err
	}
	return driver.Memory.Update(key, ttl, update)
}

// TestCacheAtomicUpdateMaintainsTagMetadata 验证创建、更新、失败和删除都在原子边界内，
// 删除后不会遗留标签的双向映射。
func TestCacheAtomicUpdateMaintainsTagMetadata(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	if err := manager.Update("counter", time.Minute, func(value interface{}, found bool) (interface{}, bool, error) {
		if found || value != nil {
			return nil, false, errors.New("首次更新不应命中")
		}
		return 1, false, nil
	}); err != nil {
		t.Fatalf("原子创建失败: %v", err)
	}
	if err := manager.UpdateContext(context.Background(), "counter", time.Minute, func(value interface{}, found bool) (interface{}, bool, error) {
		if !found || value != 1 {
			return nil, false, errors.New("原子更新未读取当前值")
		}
		return 2, false, nil
	}); err != nil {
		t.Fatalf("原子更新失败: %v", err)
	}
	callbackErr := errors.New("业务拒绝")
	if err := manager.Update("counter", time.Minute, func(interface{}, bool) (interface{}, bool, error) {
		return nil, false, callbackErr
	}); !errors.Is(err, callbackErr) {
		t.Fatalf("原子回调错误未传播: %v", err)
	}
	if value, found, err := manager.Get("counter"); err != nil || !found || value != 2 {
		t.Fatalf("失败回调改写了缓存: value=%#v found=%t err=%v", value, found, err)
	}

	tagged, err := manager.Tag("counters")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	if err = tagged.Set("counter", 3, time.Minute); err != nil {
		t.Fatalf("给原子键绑定标签失败: %v", err)
	}
	if err = manager.Update("counter", 0, func(interface{}, bool) (interface{}, bool, error) {
		return nil, true, nil
	}); err != nil {
		t.Fatalf("原子删除失败: %v", err)
	}
	for _, key := range []string{"counter", manager.tagMetaKey("counters"), manager.tagReverseKey("counter")} {
		if _, found, getErr := backend.Get(key); getErr != nil || found {
			t.Fatalf("原子删除后残留键 %q: found=%t err=%v", key, found, getErr)
		}
	}
}

// TestCacheFencedCounterPreservesTTLAndPrecision 验证信封化后的计数仍保留 TTL，
// 并在 JSON 文件后端保持 MaxInt64 精度与原有错误语义。
func TestCacheFencedCounterPreservesTTLAndPrecision(t *testing.T) {
	tests := []struct {
		name     string
		driver   func(t *testing.T) Driver
		ttl      time.Duration
		waitTime time.Duration
	}{
		{name: "memory", driver: func(*testing.T) Driver { return cacheDriver.NewMemory() }, ttl: 35 * time.Millisecond, waitTime: 60 * time.Millisecond},
		{name: "file", driver: func(t *testing.T) Driver {
			driver, err := cacheDriver.NewFile(t.TempDir())
			if err != nil {
				t.Fatalf("创建文件缓存失败: %v", err)
			}
			return driver
		}, ttl: 800 * time.Millisecond, waitTime: time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewCache(nil, test.driver(t))
			if err := manager.Set("ttl-counter", int64(1), test.ttl); err != nil {
				t.Fatalf("写入短期计数失败: %v", err)
			}
			if value, err := manager.Inc("ttl-counter", 1); err != nil || value != 2 {
				t.Fatalf("递增短期计数失败: value=%d err=%v", value, err)
			}
			time.Sleep(test.waitTime)
			if _, found, err := manager.Get("ttl-counter"); err != nil || found {
				t.Fatalf("计数更新意外延长 TTL: found=%t err=%v", found, err)
			}

			if err := manager.Set("overflow", int64(math.MaxInt64), 0); err != nil {
				t.Fatalf("写入 MaxInt64 失败: %v", err)
			}
			if _, err := manager.Inc("overflow", 1); !errors.Is(err, cacheDriver.ErrCounterOverflow) {
				t.Fatalf("MaxInt64 溢出错误不兼容: %v", err)
			}
			value, found, err := manager.Get("overflow")
			if err != nil || !found || value != int64(math.MaxInt64) {
				t.Fatalf("失败计数破坏原值: value=%#v found=%t err=%v", value, found, err)
			}
		})
	}
}

// TestIndependentFencedWritesDoNotInvalidateEachOther 验证全局序列只用于排序，
// 不会把并发写入不同业务键误判为失效冲突。
func TestIndependentFencedWritesDoNotInvalidateEachOther(t *testing.T) {
	backend := &leaseLostFencingDriver{
		Memory:  cacheDriver.NewMemory(),
		target:  "slow-key",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	backend.block.Store(true)
	slowManager := NewCache(nil, backend)
	fastManager := NewCache(nil, backend)
	slowDone := make(chan error, 1)
	go func() { slowDone <- slowManager.Set("slow-key", "slow", time.Minute) }()
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("慢写入未进入暂停点")
	}
	if err := fastManager.Set("fast-key", "fast", time.Minute); err != nil {
		close(backend.release)
		<-slowDone
		t.Fatalf("独立快写入失败: %v", err)
	}
	close(backend.release)
	if err := <-slowDone; err != nil {
		t.Fatalf("较早的独立写入被误判为失效冲突: %v", err)
	}
	for key, expected := range map[string]string{"slow-key": "slow", "fast-key": "fast"} {
		value, found, err := fastManager.Get(key)
		if err != nil || !found || value != expected {
			t.Fatalf("并发独立键结果错误: key=%s value=%#v found=%t err=%v", key, value, found, err)
		}
	}
}

// TestIndependentAtomicUpdatesDoNotUseGlobalTagLock 验证无标签键按单键原子边界并发，
// cache-backed Session 的不同 ID 不会被 store 级标签锁串行化。
func TestIndependentAtomicUpdatesDoNotUseGlobalTagLock(t *testing.T) {
	backend := &blockingIndependentUpdateDriver{
		Memory:  cacheDriver.NewMemory(),
		target:  "session:slow",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	slowManager := NewCache(nil, backend)
	fastManager := NewCache(nil, backend)
	slowDone := make(chan error, 1)
	go func() {
		slowDone <- slowManager.Update(backend.target, time.Minute, func(interface{}, bool) (interface{}, bool, error) {
			return "slow", false, nil
		})
	}()
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("慢 Session 更新未进入暂停点")
	}
	fastDone := make(chan error, 1)
	go func() {
		fastDone <- fastManager.Update("session:fast", time.Minute, func(interface{}, bool) (interface{}, bool, error) {
			return "fast", false, nil
		})
	}()
	select {
	case err := <-fastDone:
		if err != nil {
			close(backend.release)
			<-slowDone
			t.Fatalf("独立 Session 更新失败: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		close(backend.release)
		<-slowDone
		t.Fatal("独立 Session 更新被全局标签锁串行化")
	}
	close(backend.release)
	if err := <-slowDone; err != nil {
		t.Fatalf("慢 Session 更新失败: %v", err)
	}
}

// TestFencedWriteCleansCommitCrossingInvalidation 验证值提交后才观察到更高失效水位时，
// 写入返回锁丢失且只清理自己的代际，不留下短暂提交的最终残影。
func TestFencedWriteCleansCommitCrossingInvalidation(t *testing.T) {
	backend := &postCommitInvalidatingDriver{Memory: cacheDriver.NewMemory(), target: "stale"}
	manager := NewCache(nil, backend)
	backend.invalidationKey = manager.fenceInvalidationKey()
	if err := manager.Set("stale", "value", time.Minute); !errors.Is(err, ErrCacheLockLost) {
		t.Fatalf("跨越失效水位的写入必须失败: %v", err)
	}
	if _, found, err := manager.Get("stale"); err != nil || found {
		t.Fatalf("失败代际没有被精确清理: found=%t err=%v", found, err)
	}
}

// TestFencedBatchFailureRollsBackOnlyCommittedGeneration 验证批量驱动中途失败时，
// 已提交的本批代际会被回收，尚未执行和外部键保持不变。
func TestFencedBatchFailureRollsBackOnlyCommittedGeneration(t *testing.T) {
	writeErr := errors.New("第二个键写入失败")
	backend := &failingFencedBatchDriver{Memory: cacheDriver.NewMemory(), failKey: "second", err: writeErr}
	manager := NewCache(nil, backend)
	if err := backend.Set("outside", "keep", 0); err != nil {
		t.Fatalf("准备外部键失败: %v", err)
	}
	err := manager.SetMany(map[string]interface{}{"first": "A", "second": "B", "third": "C"}, time.Minute)
	if !errors.Is(err, writeErr) {
		t.Fatalf("批量失败错误未传播: %v", err)
	}
	for _, key := range []string{"first", "second", "third"} {
		if _, found, getErr := manager.Get(key); getErr != nil || found {
			t.Fatalf("失败批次残留键: key=%s found=%t err=%v", key, found, getErr)
		}
	}
	if value, found, getErr := backend.Get("outside"); getErr != nil || !found || value != "keep" {
		t.Fatalf("失败回滚破坏外部键: value=%#v found=%t err=%v", value, found, getErr)
	}
}

// TestCacheCounterStrictNumericCompatibility 覆盖所有受支持整数表示、JSON 精度和失败边界，
// 保持 Cache facade 与内置驱动原有的 errors.Is 合同一致。
func TestCacheCounterStrictNumericCompatibility(t *testing.T) {
	supported := []struct {
		name  string
		value interface{}
	}{
		{name: "int", value: int(1)},
		{name: "int8", value: int8(1)},
		{name: "int16", value: int16(1)},
		{name: "int32", value: int32(1)},
		{name: "int64", value: int64(1)},
		{name: "uint", value: uint(1)},
		{name: "uint8", value: uint8(1)},
		{name: "uint16", value: uint16(1)},
		{name: "uint32", value: uint32(1)},
		{name: "uint64", value: uint64(1)},
		{name: "float32", value: float32(1)},
		{name: "float64", value: float64(1)},
		{name: "json-number", value: json.Number("1")},
	}
	for _, test := range supported {
		t.Run(test.name, func(t *testing.T) {
			manager := NewCache(nil, cacheDriver.NewMemory())
			if err := manager.Set("counter", test.value, 0); err != nil {
				t.Fatalf("准备计数失败: %v", err)
			}
			if value, err := manager.Inc("counter", 1); err != nil || value != 2 {
				t.Fatalf("数值表示不兼容: value=%d err=%v", value, err)
			}
		})
	}
	invalid := []interface{}{uint64(math.MaxInt64) + 1, 1.5, math.NaN(), json.Number("1.5"), "1"}
	for index, value := range invalid {
		manager := NewCache(nil, cacheDriver.NewMemory())
		if err := manager.Set("counter", value, 0); err != nil {
			t.Fatalf("准备非法计数 %d 失败: %v", index, err)
		}
		if _, err := manager.Inc("counter", 1); !errors.Is(err, cacheDriver.ErrInvalidCounterValue) {
			t.Fatalf("非法计数 %d 未返回兼容错误: %v", index, err)
		}
	}
	manager := NewCache(nil, cacheDriver.NewMemory())
	if err := manager.Set("minimum", int64(math.MinInt64), 0); err != nil {
		t.Fatalf("准备 MinInt64 失败: %v", err)
	}
	if _, err := manager.Dec("minimum", 1); !errors.Is(err, cacheDriver.ErrCounterOverflow) {
		t.Fatalf("递减溢出错误不兼容: %v", err)
	}
}

// TestCacheEnvelopeAndFenceStorageValidation 验证 JSON 后端可能返回的代际类型，
// 并对伪造、缺字段或非法精确整数编码失败关闭。
func TestCacheEnvelopeAndFenceStorageValidation(t *testing.T) {
	for _, raw := range []interface{}{1, int64(2), float64(3), json.Number("4"), "5"} {
		if value, err := parseFenceGenerationValue(raw); err != nil || value == 0 {
			t.Fatalf("合法代际解析失败: raw=%#v value=%d err=%v", raw, value, err)
		}
	}
	for _, raw := range []interface{}{0, float64(1.5), json.Number("bad"), "0", true} {
		if _, err := parseFenceGenerationValue(raw); !errors.Is(err, ErrCacheFenceUnavailable) {
			t.Fatalf("非法代际未失败关闭: raw=%#v err=%v", raw, err)
		}
	}
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	malformed := []map[string]interface{}{
		{"__thinkgo_cache_envelope": cacheValueEnvelopeMarker, "generation": "0", "value": "x"},
		{"__thinkgo_cache_envelope": cacheValueEnvelopeMarker, "generation": "1"},
		{"__thinkgo_cache_envelope": cacheValueEnvelopeMarker, "generation": "1", "value": "1", "encoding": "integer:unknown"},
		{"__thinkgo_cache_envelope": cacheValueEnvelopeMarker, "generation": "1", "value": 1, "encoding": "integer:int64"},
	}
	for index, raw := range malformed {
		key := "malformed-" + string(rune('a'+index))
		if err := backend.Set(key, raw, 0); err != nil {
			t.Fatalf("准备损坏信封失败: %v", err)
		}
		if _, _, err := manager.Get(key); !errors.Is(err, ErrCorruptCacheEnvelope) {
			t.Fatalf("损坏信封未被拒绝: index=%d err=%v", index, err)
		}
	}
}

// TestCacheAtomicUpdateRepairsExpiredGeneration 验证新一代原子写入不会继承已过期值的旧标签。
func TestCacheAtomicUpdateRepairsExpiredGeneration(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	tagged, err := manager.Tag("old-generation")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	if err = tagged.Set("profile", "old", 10*time.Millisecond); err != nil {
		t.Fatalf("写入旧一代值失败: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if err = manager.Update("profile", time.Minute, func(interface{}, bool) (interface{}, bool, error) {
		return "new", false, nil
	}); err != nil {
		t.Fatalf("写入新一代值失败: %v", err)
	}
	if err = tagged.Flush(); err != nil {
		t.Fatalf("清理旧一代标签失败: %v", err)
	}
	if value, found, getErr := manager.Get("profile"); getErr != nil || !found || value != "new" {
		t.Fatalf("旧一代标签删除了新值: value=%#v found=%t err=%v", value, found, getErr)
	}
}

// TestCacheAtomicUpdateValidationAndUnsupportedDriver 验证失败关闭、上下文和输入校验边界。
func TestCacheAtomicUpdateValidationAndUnsupportedDriver(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	//lint:ignore SA1012 此处故意传入 nil，验证上下文失败关闭。
	if err := manager.UpdateContext(nil, "key", 0, func(interface{}, bool) (interface{}, bool, error) { return nil, true, nil }); !errors.Is(err, ErrInvalidCacheContext) {
		t.Fatalf("nil context 必须拒绝: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.UpdateContext(canceled, "key", 0, func(interface{}, bool) (interface{}, bool, error) { return nil, true, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消 context 必须传播: %v", err)
	}
	if err := manager.Update("key", 0, nil); !errors.Is(err, ErrNilCacheUpdate) {
		t.Fatalf("nil 原子回调必须拒绝: %v", err)
	}
	if err := manager.Update("", 0, func(interface{}, bool) (interface{}, bool, error) { return nil, true, nil }); !errors.Is(err, ErrInvalidCacheKey) {
		t.Fatalf("空键必须拒绝: %v", err)
	}
	if err := manager.Update("key", -time.Second, func(interface{}, bool) (interface{}, bool, error) { return nil, true, nil }); !errors.Is(err, ErrInvalidCacheTTL) {
		t.Fatalf("负 TTL 必须拒绝: %v", err)
	}
	unsupported := NewCache(nil, &thinkPHPStoreDriver{})
	if err := unsupported.Update("key", 0, func(interface{}, bool) (interface{}, bool, error) { return nil, true, nil }); !errors.Is(err, ErrCacheAtomicUpdateUnsupported) {
		t.Fatalf("无原子能力驱动必须失败关闭: %v", err)
	}
}

// TestCacheClearPrefixIsolationAndValidation 验证 facade 与 NamespaceDriver 组合后仍只清理目标前缀。
func TestCacheClearPrefixIsolationAndValidation(t *testing.T) {
	backend := cacheDriver.NewMemory()
	namespaced, err := NewNamespaceDriver(backend, "project:", "tag:")
	if err != nil {
		t.Fatalf("创建命名空间驱动失败: %v", err)
	}
	manager := NewCache(nil, namespaced)
	for key, value := range map[string]string{"session:a": "A", "session:b": "B", "business": "keep"} {
		if err = manager.Set(key, value, time.Minute); err != nil {
			t.Fatalf("准备前缀缓存失败: key=%s err=%v", key, err)
		}
	}
	if err = manager.ClearPrefixContext(context.Background(), "session:"); err != nil {
		t.Fatalf("上下文前缀清理失败: %v", err)
	}
	for _, key := range []string{"session:a", "session:b"} {
		if _, found, getErr := manager.Get(key); getErr != nil || found {
			t.Fatalf("前缀键清理后仍存在: key=%s found=%t err=%v", key, found, getErr)
		}
	}
	if value, found, getErr := manager.Get("business"); getErr != nil || !found || value != "keep" {
		t.Fatalf("前缀清理误删业务键: value=%#v found=%t err=%v", value, found, getErr)
	}
	if err = manager.ClearPrefix(""); !errors.Is(err, ErrInvalidCachePrefix) {
		t.Fatalf("空前缀必须拒绝: %v", err)
	}
	//lint:ignore SA1012 此处故意传入 nil，验证上下文失败关闭。
	if err = manager.ClearPrefixContext(nil, "session:"); !errors.Is(err, ErrInvalidCacheContext) {
		t.Fatalf("nil context 必须拒绝: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = manager.ClearPrefixContext(canceled, "session:"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消 context 必须传播: %v", err)
	}
	unsupported := NewCache(nil, &thinkPHPStoreDriver{})
	if err = unsupported.ClearPrefix("session:"); !errors.Is(err, ErrCacheNamespaceClearUnsupported) {
		t.Fatalf("无前缀能力驱动必须失败关闭: %v", err)
	}
}

// TestNamespaceDriverAtomicUpdateMapsPhysicalKey 验证命名空间转发原子创建与删除。
func TestNamespaceDriverAtomicUpdateMapsPhysicalKey(t *testing.T) {
	backend := cacheDriver.NewMemory()
	driver, err := NewNamespaceDriver(backend, "tenant:", "tag:")
	if err != nil {
		t.Fatalf("创建命名空间驱动失败: %v", err)
	}
	if err = driver.Update("profile", time.Minute, func(interface{}, bool) (interface{}, bool, error) {
		return "Ada", false, nil
	}); err != nil {
		t.Fatalf("命名空间原子创建失败: %v", err)
	}
	if value, found, getErr := backend.Get("tenant:profile"); getErr != nil || !found || value != "Ada" {
		t.Fatalf("物理原子键错误: value=%#v found=%t err=%v", value, found, getErr)
	}
	if err = driver.UpdateContext(context.Background(), "profile", 0, func(interface{}, bool) (interface{}, bool, error) {
		return nil, true, nil
	}); err != nil {
		t.Fatalf("命名空间上下文原子删除失败: %v", err)
	}
	if _, found, getErr := backend.Get("tenant:profile"); getErr != nil || found {
		t.Fatalf("命名空间原子删除后仍命中: found=%t err=%v", found, getErr)
	}
}
