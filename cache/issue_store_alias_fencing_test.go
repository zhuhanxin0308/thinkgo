package cache

import (
	"errors"
	"sync"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

func TestAliasClearPreservesNewWriteAfterInvalidation(t *testing.T) {
	for _, direction := range []string{"default-to-named", "named-to-default"} {
		for _, kind := range []string{"prefix", "flush"} {
			t.Run(direction+"/"+kind, func(t *testing.T) {
				backend := &blockingConditionalClearDriver{
					Memory: cacheDriver.NewMemory(), started: make(chan struct{}), release: make(chan struct{}),
				}
				manager, named := newIssue148Aliases(t, backend)
				invalidator, writer := manager, named
				if direction == "named-to-default" {
					invalidator, writer = named, manager
				}
				if err := invalidator.Set("item:one", "old", 0); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					if kind == "prefix" {
						done <- invalidator.ClearPrefix("item:")
					} else {
						done <- invalidator.Flush()
					}
					close(done)
				}()
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(backend.release) }) }
				defer func() {
					unblock()
					awaitIssue148Error(t, done)
				}()
				awaitIssue148Signal(t, backend.started, "发布失效水位后的条件扫描")
				if err := writer.Set("item:one", "new", 0); err != nil {
					t.Fatalf("失效开始后新写入失败: %v", err)
				}
				unblock()
				if err := awaitIssue148Error(t, done); err != nil {
					t.Fatalf("跨别名清理失败: %v", err)
				}
				for _, view := range []*Cache{manager, named} {
					assertIssue148Value(t, view, "item:one", "new", true)
				}
			})
		}
	}
}

func TestAliasInvalidationRejectsLateOldWrites(t *testing.T) {
	for _, direction := range []string{"default-to-named", "named-to-default"} {
		for _, kind := range []string{"forget", "prefix", "flush"} {
			t.Run(direction+"/"+kind, func(t *testing.T) {
				backend := &blockingIndependentUpdateDriver{
					Memory: cacheDriver.NewMemory(), target: "item:late",
					started: make(chan struct{}), release: make(chan struct{}),
				}
				manager, named := newIssue148Aliases(t, backend)
				writer, invalidator := manager, named
				if direction == "named-to-default" {
					writer, invalidator = named, manager
				}
				done := make(chan error, 1)
				go func() {
					done <- writer.Set(backend.target, "stale", 0)
					close(done)
				}()
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(backend.release) }) }
				defer func() {
					unblock()
					awaitIssue148Error(t, done)
				}()
				awaitIssue148Signal(t, backend.started, "旧代写入进入原子提交之前")
				var err error
				switch kind {
				case "forget":
					err = invalidator.Forget(backend.target)
				case "prefix":
					err = invalidator.ClearPrefix("item:")
				case "flush":
					err = invalidator.Flush()
				}
				if err != nil {
					t.Fatalf("另一别名发布失效失败: %v", err)
				}
				unblock()
				if err := awaitIssue148Error(t, done); !errors.Is(err, ErrCacheLockLost) {
					t.Fatalf("跨过失效边界的旧写入必须失败: %v", err)
				}
				for _, view := range []*Cache{manager, named} {
					assertIssue148Value(t, view, backend.target, nil, false)
				}
				if err := invalidator.Set(backend.target, "fresh", 0); err != nil {
					t.Fatal(err)
				}
				assertIssue148Value(t, writer, backend.target, "fresh", true)
			})
		}
	}
}

func awaitIssue148Error(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("缓存并发操作未完成")
		return nil
	}
}
