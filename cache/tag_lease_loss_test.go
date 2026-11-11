package cache

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// tagLeaseBarrier 暂停读取后的旧快照或已经进入驱动的写入，模拟另一进程接管。
type tagLeaseBarrier struct {
	*cacheDriver.Memory
	phase      string
	armed      atomic.Bool
	lost       atomic.Bool
	entered    chan struct{}
	resume     chan struct{}
	lockKey    string
	owner      string
	renewError error
}

func (backend *tagLeaseBarrier) pause(phase, key string) {
	if backend.phase == phase && strings.Contains(key, tagMetaPrefix) && backend.armed.CompareAndSwap(true, false) {
		close(backend.entered)
		<-backend.resume
	}
}

func (backend *tagLeaseBarrier) Get(key string) (interface{}, bool, error) {
	value, found, err := backend.Memory.Get(key)
	backend.pause("read", key)
	return value, found, err
}

func (backend *tagLeaseBarrier) Set(key string, value interface{}, ttl time.Duration) error {
	backend.pause("write", key)
	return backend.Memory.Set(key, value, ttl)
}

func (backend *tagLeaseBarrier) UpdateIfLockOwnerContext(ctx context.Context, key string, ttl time.Duration, lockKey, owner string, update func(interface{}, bool) (interface{}, bool, error)) (bool, error) {
	backend.pause("write", key)
	return backend.Memory.UpdateIfLockOwnerContext(ctx, key, ttl, lockKey, owner, update)
}

func (backend *tagLeaseBarrier) AcquireLock(key, owner string, ttl time.Duration) (bool, error) {
	acquired, err := backend.Memory.AcquireLock(key, owner, ttl)
	if acquired {
		backend.lockKey, backend.owner = key, owner
	}
	return acquired, err
}

func (backend *tagLeaseBarrier) RenewLock(key, owner string, ttl time.Duration) (bool, error) {
	if backend.lost.Load() {
		return false, backend.renewError
	}
	return backend.Memory.RenewLock(key, owner, ttl)
}

// TestTagLeaseLossCannotOverwriteNewOwner 验证旧回调及其回滚不能删除新持有者已提交的成员。
func TestTagLeaseLossCannotOverwriteNewOwner(t *testing.T) {
	for _, phase := range []string{"read", "write"} {
		for _, notification := range []string{"尚未续租", "续租失主", "续租错误"} {
			synctest.Test(t, func(t *testing.T) {
				backend := &tagLeaseBarrier{Memory: cacheDriver.NewMemory(), phase: phase,
					entered: make(chan struct{}), resume: make(chan struct{})}
				if notification == "续租错误" {
					backend.renewError = errors.New("模拟续租传输失败")
				}
				first := NewCache(nil, backend)
				second := NewCache(nil, backend.Memory)
				taggedFirst, err := first.Tag("team")
				if err != nil {
					t.Fatal(err)
				}
				taggedSecond, err := second.Tag("team")
				if err != nil {
					t.Fatal(err)
				}
				if err := taggedSecond.Set("seed", "initial"); err != nil {
					t.Fatal(err)
				}
				backend.armed.Store(true)
				done := make(chan error, 1)
				go func() { done <- taggedFirst.Set("a", "old-owner") }()
				<-backend.entered
				if released, err := backend.Memory.ReleaseLock(backend.lockKey, backend.owner); err != nil || !released {
					t.Fatalf("模拟释放旧租约失败: %v", err)
				}
				backend.lost.Store(true)
				if err := taggedSecond.Set("b", "new-owner"); err != nil {
					t.Fatal(err)
				}
				if notification != "尚未续租" {
					time.Sleep(tagMutationLockTTL / 3)
					synctest.Wait()
				}
				close(backend.resume)
				if err := <-done; !errors.Is(err, ErrCacheLockLost) {
					t.Errorf("旧回调必须报告失锁: %v", err)
				}
				members, err := second.tagMembersLockedContext(context.Background(), backend.Memory, "team")
				if err != nil || !containsString(members, "b") || containsString(members, "a") {
					t.Errorf("旧快照覆盖了新成员: members=%v err=%v", members, err)
				}
				if err := taggedSecond.Flush(); err != nil {
					t.Fatal(err)
				}
				if _, found, err := second.Get("b"); err != nil || found {
					t.Errorf("标签清理遗漏新持有者业务值: found=%v err=%v", found, err)
				}
			})
		}
	}
}

// TestTagMetadataRejectsUnguardedDistributedDriver 验证旧锁驱动不会静默执行不安全的标签提交。
func TestTagMetadataRejectsUnguardedDistributedDriver(t *testing.T) {
	backend := &distributedOnlyDriver{memory: cacheDriver.NewMemory()}
	manager := NewCache(nil, backend)
	if err := manager.Set("plain", "value"); err != nil {
		t.Fatalf("旧驱动的普通写入应保持兼容: %v", err)
	}
	tagged, err := manager.Tag("team")
	if err != nil {
		t.Fatal(err)
	}
	if err := tagged.Set("tagged", "value"); !errors.Is(err, ErrCacheLockUnsupported) {
		t.Fatalf("缺少原子所有权能力必须拒绝标签写入: %v", err)
	}
	if _, found, err := backend.Get("tagged"); err != nil || found {
		t.Fatalf("不支持的标签写入不能留下业务值: found=%v err=%v", found, err)
	}
}

// TestTagMutationCancellationStopsMetadata 验证回调执行中的请求取消不会继续写入成员。
func TestTagMutationCancellationStopsMetadata(t *testing.T) {
	backend := &tagLeaseBarrier{Memory: cacheDriver.NewMemory(), phase: "read",
		entered: make(chan struct{}), resume: make(chan struct{})}
	manager := NewCache(nil, backend)
	tagged, err := manager.Tag("team")
	if err != nil {
		t.Fatal(err)
	}
	backend.armed.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- tagged.SetContext(ctx, "cancelled", "value") }()
	<-backend.entered
	cancel()
	close(backend.resume)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("执行中的取消未传播: %v", err)
	}
	if _, found, err := backend.Get(manager.tagMetaKey("team")); err != nil || found {
		t.Fatalf("取消后仍写入标签成员: found=%v err=%v", found, err)
	}
}
