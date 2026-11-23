package cache

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func flushTestTags(t *testing.T, manager *Cache, tags ...string) *TaggedCache {
	t.Helper()
	tagged, err := manager.Tag(tags...)
	if err != nil {
		t.Fatal(err)
	}
	return tagged
}

func TestTagFlushPublishesOnlyProvenMembers(t *testing.T) {
	for name, factory := range leaseTestBackends() {
		t.Run(name+"/orphan", func(t *testing.T) {
			driver := factory(t)
			manager := NewCache(nil, driver)
			if err := flushTestTags(t, manager, "actual").Set("healthy", "keep"); err != nil {
				t.Fatal(err)
			}
			if err := driver.Set(manager.tagMetaKey("orphan"), []string{"healthy"}, 0); err != nil {
				t.Fatal(err)
			}
			if err := flushTestTags(t, manager, "orphan").Flush(); err != nil {
				t.Fatal(err)
			}
			if value, found, err := manager.Get("healthy"); err != nil || !found || value != "keep" {
				t.Errorf("孤立正向边使健康值不可读：%v %t %v", value, found, err)
			}
			if fence, err := manager.invalidationForKey(t.Context(), driver, "healthy"); err != nil || fence != 0 {
				t.Errorf("孤边不应写入精确失效水位：%d %v", fence, err)
			}
			if tags, err := manager.reverseTagsLockedContext(t.Context(), driver, "healthy"); err != nil || !slices.Equal(tags, []string{"actual"}) {
				t.Errorf("健康键的真实标签被清理：%v %v", tags, err)
			}
			if members, err := manager.tagMembersLockedContext(t.Context(), driver, "orphan"); err != nil || len(members) != 0 {
				t.Errorf("孤立正向边仍在：%v %v", members, err)
			}
		})
		t.Run(name+"/union", func(t *testing.T) {
			driver := factory(t)
			manager := NewCache(nil, driver)
			first := flushTestTags(t, manager, "one")
			for _, key := range []string{"both", "absent"} {
				if err := first.Set(key, "old"); err != nil {
					t.Fatal(err)
				}
			}
			if err := manager.Set("healthy", "keep"); err != nil {
				t.Fatal(err)
			}
			if err := driver.Set(manager.tagMetaKey("two"), []string{"healthy", "both"}, 0); err != nil {
				t.Fatal(err)
			}
			late, found, err := driver.Get("absent")
			if err != nil || !found {
				t.Fatal(found, err)
			}
			if err := driver.Delete("absent"); err != nil {
				t.Fatal(err)
			}
			if err := flushTestTags(t, manager, "one", "two").Flush(); err != nil {
				t.Fatal(err)
			}
			if _, found, err := driver.Get("both"); err != nil || found {
				t.Errorf("有效双向边和孤边指向同一键时，应删除并集中的成员：%t %v", found, err)
			}
			if err := driver.Set("absent", late, 0); err != nil {
				t.Fatal(err)
			}
			values, err := manager.GetMany([]string{"both", "absent", "healthy"})
			if err != nil || len(values) != 1 || values["healthy"] != "keep" {
				t.Errorf("联合清理后留下了失效的迟到值或隐藏了孤边所指的健康值：%v %v", values, err)
			}
			for _, key := range []string{manager.tagMetaKey("one"), manager.tagMetaKey("two"), manager.tagReverseKey("both"), manager.tagReverseKey("absent")} {
				if _, found, err := driver.Get(key); err != nil || found {
					t.Errorf("联合清理后仍有元数据 %q：%t %v", key, found, err)
				}
			}
		})
		t.Run(name+"/corrupt-reverse", func(t *testing.T) {
			driver := factory(t)
			manager := NewCache(nil, driver)
			tagged := flushTestTags(t, manager, "one")
			keys := []string{"valid", "broken"}
			for _, key := range keys {
				if err := tagged.Set(key, key); err != nil {
					t.Fatal(err)
				}
			}
			if err := driver.Set(manager.tagReverseKey("broken"), "bad", 0); err != nil {
				t.Fatal(err)
			}
			if err := tagged.Flush(); !errors.Is(err, ErrCorruptTagMetadata) {
				t.Fatalf("未报告反向元数据错误：%v", err)
			}
			for _, key := range keys {
				if fence, err := manager.invalidationForKey(t.Context(), driver, key); err != nil || fence != 0 {
					t.Errorf("核验失败前已发布 %q 的水位：%d %v", key, fence, err)
				}
				if value, found, err := manager.Get(key); err != nil || !found || value != key {
					t.Errorf("核验失败影响了 %q 的可见性：%v %t %v", key, value, found, err)
				}
			}
		})
	}
}

type flushProofReader struct {
	Driver
	key    string
	paused func(context.Context)
}

type flushProofBarrier struct {
	*flushProofReader
	AtomicUpdater
	TTLAtomicUpdater
	DistributedLocker
	LockRenewer
	LockGuardedUpdater
}

func (driver *flushProofReader) GetContext(ctx context.Context, key string) (interface{}, bool, error) {
	value, found, err := getCacheValueContext(ctx, driver.Driver, key)
	if key == driver.key && driver.paused != nil {
		paused := driver.paused
		driver.paused = nil
		paused(ctx)
	}
	return value, found, err
}

func TestTagFlushRejectsLostOwnerAfterReverseRead(t *testing.T) {
	for name, factory := range leaseTestBackends() {
		for _, mode := range []string{"lose", "rewrite"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				driver := factory(t)
				manager := NewCache(nil, driver)
				const key = "account"
				if err := flushTestTags(t, manager, "accounts").Set(key, "original"); err != nil {
					t.Fatal(err)
				}
				barrier := &flushProofBarrier{
					flushProofReader:   &flushProofReader{Driver: driver, key: manager.tagReverseKey(key)},
					AtomicUpdater:      driver.(AtomicUpdater),
					TTLAtomicUpdater:   driver.(TTLAtomicUpdater),
					DistributedLocker:  driver.(DistributedLocker),
					LockRenewer:        driver.(LockRenewer),
					LockGuardedUpdater: driver.(LockGuardedUpdater),
				}
				var expectedFence uint64
				expectedValue := "original"
				// 读取已离开后端的锁，暂停在返回旧反向快照之前；第二个管理器可以完成整个接管过程。
				barrier.paused = func(ctx context.Context) {
					if fence, err := manager.invalidationForKey(t.Context(), driver, key); err != nil || fence != 0 {
						t.Errorf("取得反向证明前已改变可见性：%d %v", fence, err)
					}
					lease := ctx.Value(tagMutationLeaseKey{}).(tagMutationLease)
					if ok, err := barrier.DistributedLocker.ReleaseLock(lease.key, lease.owner); err != nil || !ok {
						t.Fatal(ok, err)
					}
					if mode == "rewrite" {
						replacement, cancel := context.WithTimeout(t.Context(), tagMutationWait)
						defer cancel()
						if err := manager.ForgetContext(replacement, key); err != nil {
							t.Fatal(err)
						}
						if err := manager.SetContext(replacement, key, "new"); err != nil {
							t.Fatal(err)
						}
						var err error
						expectedFence, err = manager.invalidationForKey(replacement, driver, key)
						if err != nil {
							t.Fatal(err)
						}
						expectedValue = "new"
					}
				}
				if err := flushTestTags(t, NewCache(nil, barrier), "accounts").Flush(); !errors.Is(err, ErrCacheLockLost) {
					t.Errorf("失主未报告清理失败：%v", err)
				}
				if barrier.paused != nil {
					t.Fatal("未经过反向证明读取")
				}
				if value, found, err := manager.Get(key); err != nil || !found || value != expectedValue {
					t.Errorf("失去租约的持有者仍能让值失效：%v %t %v", value, found, err)
				}
				if fence, err := manager.invalidationForKey(t.Context(), driver, key); err != nil || fence != expectedFence {
					t.Errorf("失主修改了有效水位：%d，预期 %d，%v", fence, expectedFence, err)
				}
			})
		}
	}
}

func TestTagFlushKeepsEarlierTicketWithoutGuardedUpdates(t *testing.T) {
	for name, factory := range leaseTestBackends() {
		t.Run(name, func(t *testing.T) {
			driver := factory(t)
			const key = "account"
			manager := NewCache(nil, driver)
			if err := flushTestTags(t, manager, "accounts").Set(key, "old"); err != nil {
				t.Fatal(err)
			}
			// 旧驱动能加锁和更新水位，不能以租约原子提交。另一个只提供单键原子的客户端可并发写入。
			reader := &flushProofReader{Driver: driver, key: manager.tagReverseKey(key)}
			legacy := struct {
				*flushProofReader
				AtomicUpdater
				TTLAtomicUpdater
				DistributedLocker
			}{reader, driver.(AtomicUpdater), driver.(TTLAtomicUpdater), driver.(DistributedLocker)}
			concurrent := &legacyTTLAtomicDriver{Driver: driver, AtomicUpdater: driver.(AtomicUpdater), TTLAtomicUpdater: driver.(TTLAtomicUpdater)}
			reader.paused = func(context.Context) {
				write, cancel := context.WithTimeout(t.Context(), tagMutationWait)
				defer cancel()
				if err := NewCache(nil, concurrent).SetContext(write, key, "new"); err != nil {
					t.Fatal(err)
				}
			}
			if err := flushTestTags(t, NewCache(nil, legacy), "accounts").Flush(); err != nil {
				t.Fatalf("已有单键原子操作被新增能力要求拒绝：%v", err)
			}
			if value, found, err := manager.Get(key); err != nil || !found || value != "new" {
				t.Fatalf("旧持有者的票号覆盖了并发新值：%v %t %v", value, found, err)
			}
		})
	}
}

func TestTagFlushRejectsLostOwnerWithoutFencing(t *testing.T) {
	for name, factory := range leaseTestBackends() {
		t.Run(name, func(t *testing.T) {
			driver := factory(t)
			reader := &flushProofReader{Driver: driver}
			// 只提供租约原子写回的驱动没有失效水位，删除业务键仍可由同一 owner 校验保护。
			limited := struct {
				*flushProofReader
				DistributedLocker
				LockGuardedUpdater
			}{reader, driver.(DistributedLocker), driver.(LockGuardedUpdater)}
			manager := NewCache(nil, limited)
			tagged := flushTestTags(t, manager, "accounts")
			const key = "account"
			if err := tagged.Set(key, "original"); err != nil {
				t.Fatal(err)
			}
			reader.key = manager.tagReverseKey(key)
			reader.paused = func(ctx context.Context) {
				lease := ctx.Value(tagMutationLeaseKey{}).(tagMutationLease)
				if ok, err := limited.ReleaseLock(lease.key, lease.owner); err != nil || !ok {
					t.Fatal(ok, err)
				}
			}
			if err := tagged.Flush(); !errors.Is(err, ErrCacheLockLost) {
				t.Fatalf("失主未收到租约错误：%v", err)
			}
			if value, found, err := driver.Get(key); err != nil || !found || value != "original" {
				t.Fatalf("失主删除了无水位后端的数据：%v %t %v", value, found, err)
			}
		})
	}
}
