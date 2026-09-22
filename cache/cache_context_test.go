package cache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

type cacheContextKey struct{}

type legacyRenewingLocker struct{}

func (*legacyRenewingLocker) AcquireLock(string, string, time.Duration) (bool, error) {
	return true, nil
}

func (*legacyRenewingLocker) ReleaseLock(string, string) (bool, error) {
	return true, nil
}

func (*legacyRenewingLocker) RenewLock(string, string, time.Duration) (bool, error) {
	return true, nil
}

type contextTrackingCacheDriver struct {
	*cacheDriver.Memory
	mu             sync.Mutex
	getContext     context.Context
	setContext     context.Context
	getManyContext context.Context
	setManyContext context.Context
	hasContext     context.Context
	deleteContext  context.Context
	atomicContext  context.Context
	clearContext   context.Context
	lockContext    context.Context
}

func (driver *contextTrackingCacheDriver) GetContext(ctx context.Context, key string) (interface{}, bool, error) {
	driver.mu.Lock()
	driver.getContext = ctx
	driver.mu.Unlock()
	return driver.Memory.Get(key)
}

func (driver *contextTrackingCacheDriver) SetContext(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	driver.mu.Lock()
	driver.setContext = ctx
	driver.mu.Unlock()
	return driver.Memory.Set(key, value, ttl)
}

func (driver *contextTrackingCacheDriver) GetManyContext(ctx context.Context, keys []string) (map[string]interface{}, error) {
	driver.mu.Lock()
	driver.getManyContext = ctx
	driver.mu.Unlock()
	values := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		value, found, err := driver.Memory.Get(key)
		if err != nil {
			return nil, err
		}
		if found {
			values[key] = value
		}
	}
	return values, nil
}

func (driver *contextTrackingCacheDriver) SetManyContext(ctx context.Context, values map[string]interface{}, ttl time.Duration) error {
	driver.mu.Lock()
	driver.setManyContext = ctx
	driver.mu.Unlock()
	for key, value := range values {
		if err := driver.Memory.Set(key, value, ttl); err != nil {
			return err
		}
	}
	return nil
}

func (driver *contextTrackingCacheDriver) HasContext(ctx context.Context, key string) (bool, error) {
	driver.mu.Lock()
	driver.hasContext = ctx
	driver.mu.Unlock()
	return driver.Memory.Has(key)
}

func (driver *contextTrackingCacheDriver) DeleteContext(ctx context.Context, key string) error {
	driver.mu.Lock()
	driver.deleteContext = ctx
	driver.mu.Unlock()
	return driver.Memory.Delete(key)
}

func (driver *contextTrackingCacheDriver) UpdateContext(ctx context.Context, key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	driver.mu.Lock()
	driver.atomicContext = ctx
	driver.mu.Unlock()
	return driver.Memory.Update(key, ttl, update)
}

func (driver *contextTrackingCacheDriver) ClearContext(ctx context.Context) error {
	driver.mu.Lock()
	driver.clearContext = ctx
	driver.mu.Unlock()
	return driver.Memory.Clear()
}

func (driver *contextTrackingCacheDriver) ClearPrefixContext(ctx context.Context, prefix string) error {
	driver.mu.Lock()
	driver.clearContext = ctx
	driver.mu.Unlock()
	return driver.Memory.ClearPrefix(prefix)
}

func (driver *contextTrackingCacheDriver) ClearPrefixIfContext(ctx context.Context, prefix string, match func(string) bool, remove func(interface{}) (bool, error)) error {
	driver.mu.Lock()
	driver.clearContext = ctx
	driver.mu.Unlock()
	return driver.Memory.ClearPrefixIfContext(ctx, prefix, match, remove)
}

func (driver *contextTrackingCacheDriver) AcquireLockContext(ctx context.Context, key string, owner string, ttl time.Duration) (bool, error) {
	driver.mu.Lock()
	driver.lockContext = ctx
	driver.mu.Unlock()
	return driver.Memory.AcquireLock(key, owner, ttl)
}

func (driver *contextTrackingCacheDriver) ReleaseLockContext(ctx context.Context, key string, owner string) (bool, error) {
	return driver.Memory.ReleaseLock(key, owner)
}

func (driver *contextTrackingCacheDriver) RenewLockContext(ctx context.Context, key string, owner string, ttl time.Duration) (bool, error) {
	return driver.Memory.RenewLock(key, owner, ttl)
}

func (driver *contextTrackingCacheDriver) contexts() (context.Context, context.Context, context.Context, context.Context, context.Context, context.Context, context.Context, context.Context) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return driver.getContext, driver.setContext, driver.getManyContext, driver.setManyContext, driver.hasContext, driver.deleteContext, driver.clearContext, driver.lockContext
}

func (driver *contextTrackingCacheDriver) lastAtomicContext() context.Context {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return driver.atomicContext
}

// TestCacheContextMethodsPropagateContext 验证缓存 facade 会把上下文传递给可选驱动接口。
func TestCacheContextMethodsPropagateContext(t *testing.T) {
	driver := &contextTrackingCacheDriver{Memory: cacheDriver.NewMemory()}
	manager := NewCache(nil, driver)
	ctx := context.WithValue(context.Background(), cacheContextKey{}, "request-1")
	if err := manager.SetContext(ctx, "context-key", "value", time.Minute); err != nil {
		t.Fatalf("SetContext 失败: %v", err)
	}
	if err := manager.SetManyContext(ctx, map[string]interface{}{"batch-key": "batch-value"}, time.Minute); err != nil {
		t.Fatalf("SetManyContext 失败: %v", err)
	}
	if value, found, err := manager.GetContext(ctx, "context-key"); err != nil || !found || value != "value" {
		t.Fatalf("GetContext 结果错误: value=%#v found=%t err=%v", value, found, err)
	}
	if values, err := manager.GetManyContext(ctx, []string{"batch-key"}); err != nil || values["batch-key"] != "batch-value" {
		t.Fatalf("GetManyContext 结果错误: values=%#v err=%v", values, err)
	}
	lock, err := manager.Lock("context-lock", time.Minute)
	if err != nil {
		t.Fatalf("创建上下文锁失败: %v", err)
	}
	if acquired, err := lock.AcquireContext(ctx); err != nil || !acquired {
		t.Fatalf("AcquireContext 失败: acquired=%t err=%v", acquired, err)
	}
	_, _, getManyContext, _, _, _, _, lockContext := driver.contexts()
	if updateContext := driver.lastAtomicContext(); updateContext == nil || updateContext.Value(cacheContextKey{}) != "request-1" {
		t.Fatal("写入原子 UpdateContext 未收到原始上下文")
	}
	if getManyContext == nil || getManyContext.Value(cacheContextKey{}) != "request-1" {
		t.Fatal("GetManyContext 未收到原始上下文")
	}
	getContext, _, _, _, _, _, _, _ := driver.contexts()
	if getContext == nil || getContext.Value(cacheContextKey{}) != "request-1" {
		t.Fatal("GetContext 未收到原始上下文")
	}
	if lockContext == nil || lockContext.Value(cacheContextKey{}) != "request-1" {
		t.Fatal("AcquireContext 未收到原始上下文")
	}
	if released, err := lock.ReleaseContext(context.Background()); err != nil || !released {
		t.Fatalf("释放上下文锁失败: released=%t err=%v", released, err)
	}
}

// TestCacheContextMutationMethodsPropagateContext 验证查询上下文能够贯穿 Has、Forget 和 Flush。
func TestCacheContextMutationMethodsPropagateContext(t *testing.T) {
	driver := &contextTrackingCacheDriver{Memory: cacheDriver.NewMemory()}
	manager := NewCache(nil, driver)
	ctx := context.WithValue(context.Background(), cacheContextKey{}, "mutation-context")
	if err := manager.SetContext(ctx, "mutation-key", "value", time.Minute); err != nil {
		t.Fatalf("预置缓存失败: %v", err)
	}
	if found, err := manager.HasContext(ctx, "mutation-key"); err != nil || !found {
		t.Fatalf("HasContext 失败: found=%t err=%v", found, err)
	}
	if err := manager.ForgetContext(ctx, "mutation-key"); err != nil {
		t.Fatalf("ForgetContext 失败: %v", err)
	}
	if err := manager.FlushContext(ctx); err != nil {
		t.Fatalf("FlushContext 失败: %v", err)
	}
	_, _, _, _, hasContext, _, clearContext, _ := driver.contexts()
	for name, received := range map[string]context.Context{
		"HasContext":    hasContext,
		"UpdateContext": driver.lastAtomicContext(),
		"ClearContext":  clearContext,
	} {
		if received == nil || received.Value(cacheContextKey{}) != "mutation-context" {
			t.Fatalf("%s 未收到调用方上下文", name)
		}
	}
}

// TestTaggedMutationContextCancelsLockWait 验证标签锁竞争会在请求取消后及时返回。
func TestTaggedMutationContextCancelsLockWait(t *testing.T) {
	driver := &distributedOnlyDriver{memory: cacheDriver.NewMemory()}
	manager := NewCache(nil, driver)
	lock, err := manager.Lock("tag:"+defaultStoreName, time.Minute)
	if err != nil {
		t.Fatalf("创建标签锁失败: %v", err)
	}
	if acquired, err := lock.Acquire(); err != nil || !acquired {
		t.Fatalf("预置标签锁失败: acquired=%t err=%v", acquired, err)
	}
	defer lock.Release()
	tagged, err := manager.Tag("users")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := tagged.SetContext(ctx, "user:cancelled", "value", time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("标签锁等待取消应返回 DeadlineExceeded，实际为 %v", err)
	}
	if _, found, readErr := manager.Get("user:cancelled"); readErr != nil || found {
		t.Fatalf("取消的标签写入不应留下业务键: found=%t err=%v", found, readErr)
	}
}

// TestRememberWithLockContextCancelsLockWait 验证请求取消会中断分布式锁等待且不执行加载回调。
func TestRememberWithLockContextCancelsLockWait(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	lock, err := manager.Lock("busy-context", time.Minute)
	if err != nil {
		t.Fatalf("创建占用锁失败: %v", err)
	}
	if acquired, err := lock.Acquire(); err != nil || !acquired {
		t.Fatalf("预置占用锁失败: acquired=%t err=%v", acquired, err)
	}
	defer lock.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	callbackCalled := false
	_, err = manager.RememberWithLockContext(ctx, "busy-context", time.Minute, time.Second, func() (interface{}, error) {
		callbackCalled = true
		return "unexpected", nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("锁等待取消应返回 DeadlineExceeded，实际为 %v", err)
	}
	if callbackCalled {
		t.Fatal("锁等待被取消后不应执行加载回调")
	}
}

// TestRememberContextCancelsSharedWait 验证共享加载未完成时等待者可以独立响应请求取消。
func TestRememberContextCancelsSharedWait(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	started := make(chan struct{})
	finish := make(chan struct{})
	ownerDone := make(chan error, 1)
	go func() {
		_, err := manager.RememberContext(context.Background(), "shared-context", time.Minute, func() (interface{}, error) {
			close(started)
			<-finish
			return "value", nil
		})
		ownerDone <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := manager.RememberContext(ctx, "shared-context", time.Minute, func() (interface{}, error) {
		t.Fatal("共享加载等待者不应执行重复回调")
		return nil, nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("共享加载等待应返回 DeadlineExceeded，实际为 %v", err)
	}
	close(finish)
	if err := <-ownerDone; err != nil {
		t.Fatalf("共享加载所有者失败: %v", err)
	}
	if value, found, err := manager.Get("shared-context"); err != nil || !found || value != "value" {
		t.Fatalf("共享加载完成后缓存结果错误: value=%#v found=%t err=%v", value, found, err)
	}
}

// TestCacheContextMethodsRejectNilContext 验证上下文 API 不会静默接受 nil 上下文。
func TestCacheContextMethodsRejectNilContext(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	var nilContext context.Context
	if _, _, err := manager.GetContext(nilContext, "key"); !errors.Is(err, ErrInvalidCacheContext) {
		t.Fatalf("nil GetContext 应返回 ErrInvalidCacheContext，实际为 %v", err)
	}
	if err := manager.SetContext(nilContext, "key", "value", time.Minute); !errors.Is(err, ErrInvalidCacheContext) {
		t.Fatalf("nil SetContext 应返回 ErrInvalidCacheContext，实际为 %v", err)
	}
	if _, err := manager.GetManyContext(nilContext, []string{"key"}); !errors.Is(err, ErrInvalidCacheContext) {
		t.Fatalf("nil GetManyContext 应返回 ErrInvalidCacheContext，实际为 %v", err)
	}
	if err := manager.SetManyContext(nilContext, map[string]interface{}{"key": "value"}, time.Minute); !errors.Is(err, ErrInvalidCacheContext) {
		t.Fatalf("nil SetManyContext 应返回 ErrInvalidCacheContext，实际为 %v", err)
	}
	if _, err := manager.RememberContext(nilContext, "key", time.Minute, func() (interface{}, error) {
		return "value", nil
	}); !errors.Is(err, ErrInvalidCacheContext) {
		t.Fatalf("nil RememberContext 应返回 ErrInvalidCacheContext，实际为 %v", err)
	}
	if _, err := manager.RememberWithLockContext(nilContext, "key", time.Minute, time.Second, func() (interface{}, error) {
		return "value", nil
	}); !errors.Is(err, ErrInvalidCacheContext) {
		t.Fatalf("nil RememberWithLockContext 应返回 ErrInvalidCacheContext，实际为 %v", err)
	}
}

// TestCacheContextMethodsRejectCanceledContext 验证请求取消会在访问驱动前统一返回。
func TestCacheContextMethodsRejectCanceledContext(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.HasContext(ctx, "key"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 HasContext 应返回 context.Canceled，实际为 %v", err)
	}
	if err := manager.ForgetContext(ctx, "key"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 ForgetContext 应返回 context.Canceled，实际为 %v", err)
	}
	if err := manager.FlushContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 FlushContext 应返回 context.Canceled，实际为 %v", err)
	}
}

// TestLegacyLockRenewalCompatibility 验证仅实现旧锁接口的驱动仍能完成续租。
func TestLegacyLockRenewalCompatibility(t *testing.T) {
	locker := &legacyRenewingLocker{}
	renewed, err := renewDistributedLock(context.Background(), locker, locker, "key", "owner", time.Second)
	if err != nil || !renewed {
		t.Fatalf("旧锁接口续租失败: renewed=%t err=%v", renewed, err)
	}
}
