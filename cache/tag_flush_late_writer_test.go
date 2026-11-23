package cache

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

type beforeFlushValueRead struct {
	*cacheDriver.Memory
	key    string
	before func()
}

func (driver *beforeFlushValueRead) Get(key string) (interface{}, bool, error) {
	if key == driver.key && driver.before != nil {
		before := driver.before
		driver.before = nil
		before()
	}
	return driver.Memory.Get(key)
}

func TestTagFlushFencesLateWriterAfterNewerValueExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const key, tag = "account", "accounts"
		const ttl = time.Minute
		memory := cacheDriver.NewMemory()
		manager := NewCache(nil, memory)
		// 两个普通写者均已观察到键没有标签。旧写者已领票，新写者在返回空的反向关系前暂停。
		old := &blockingIndependentUpdateDriver{Memory: memory, target: key, started: make(chan struct{}), release: make(chan struct{})}
		releaseOld := sync.OnceFunc(func() { close(old.release) })
		defer releaseOld()
		oldResult := make(chan error, 1)
		go func() { oldResult <- NewCache(nil, old).Set(key, "late") }()
		<-old.started
		fresh := &pausedGlobalReadDriver{Memory: memory, target: manager.tagReverseKey(key), started: make(chan struct{}), release: make(chan struct{})}
		releaseFresh := sync.OnceFunc(func() { close(fresh.release) })
		defer releaseFresh()
		freshResult := make(chan error, 1)
		go func() { freshResult <- NewCache(nil, fresh).Set(key, "fresh", ttl) }()
		<-fresh.started
		if err := flushTestTags(t, manager, tag).Set(key, "member"); err != nil {
			t.Fatal(err)
		}
		// 清理已领票。让较新的普通写者先提交，清理应保留其值和标签，同时隔离旧写者。
		flush := &beforeFlushValueRead{Memory: memory, key: key, before: func() {
			releaseFresh()
			if err := <-freshResult; err != nil {
				t.Fatal(err)
			}
		}}
		if err := flushTestTags(t, NewCache(nil, flush), tag).Flush(); err != nil {
			t.Fatal(err)
		}
		if value, found, err := manager.Get(key); err != nil || !found || value != "fresh" {
			t.Fatalf("清理误伤较新写入：%v %t %v", value, found, err)
		}
		if tags, err := manager.reverseTagsLockedContext(t.Context(), memory, key); err != nil || !slices.Equal(tags, []string{tag}) {
			t.Fatalf("较新值失去反向标签关系：%v %v", tags, err)
		}
		if keys, err := manager.tagMembersLockedContext(t.Context(), memory, tag); err != nil || !slices.Equal(keys, []string{key}) {
			t.Fatalf("较新值失去正向标签关系：%v %v", keys, err)
		}
		time.Sleep(ttl)
		if _, found, err := manager.Get(key); err != nil || found {
			t.Fatalf("短期值应已自然过期：%t %v", found, err)
		}
		releaseOld()
		if err := <-oldResult; !errors.Is(err, ErrCacheLockLost) {
			t.Errorf("清理前领票的旧写者未被失效水位拒绝：%v", err)
		}
		if value, found, err := manager.Get(key); err != nil || found {
			t.Errorf("新值过期后旧写者重新可见：%v %t %v", value, found, err)
		}
	})
}
