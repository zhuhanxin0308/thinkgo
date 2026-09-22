package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type conditionalCacheDriver interface {
	atomicCacheDriver
	ClearPrefixIfContext(context.Context, string, func(string) bool, func(interface{}) (bool, error)) error
}

// TestConditionalClearKeepsScopeTTLAndNewValues 验证后端只删除匹配且满足代际条件的值，保留其余值与 TTL。
func TestConditionalClearKeepsScopeTTLAndNewValues(t *testing.T) {
	file, _ := newTestFileDriver(t)
	for name, backend := range map[string]conditionalCacheDriver{"memory": NewMemory(), "file": file, "redis": newAtomicRedisDriver(t)} {
		t.Run(name, func(t *testing.T) {
			for key, value := range map[string]string{"scope:old": "old", "scope:new": "new", "scope:skip": "old", "other:old": "old", cacheFenceMetadataPrefix + "sequence": "5"} {
				if err := backend.Set(key, value, time.Minute); err != nil {
					t.Fatal(err)
				}
			}
			if err := backend.ClearPrefixIfContext(context.Background(), "scope:", func(key string) bool { return key != "scope:skip" }, func(value interface{}) (bool, error) { return value == "old", nil }); err != nil {
				t.Fatal(err)
			}
			for key, wantFound := range map[string]bool{"scope:old": false, "scope:new": true, "scope:skip": true, "other:old": true, cacheFenceMetadataPrefix + "sequence": true} {
				if _, found, err := backend.Get(key); err != nil || found != wantFound {
					t.Fatalf("条件清理范围错误: key=%s found=%t err=%v", key, found, err)
				}
			}
			callbackErr := errors.New("拒绝本轮清理")
			if err := backend.ClearPrefixIfContext(context.Background(), "scope:", nil, func(interface{}) (bool, error) { return false, callbackErr }); !errors.Is(err, callbackErr) {
				t.Fatalf("清理回调错误丢失: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := backend.ClearPrefixIfContext(ctx, "scope:", nil, func(interface{}) (bool, error) { return true, nil }); !errors.Is(err, context.Canceled) {
				t.Fatalf("取消清理仍继续操作: %v", err)
			}
			if _, found, err := backend.Get("scope:new"); err != nil || !found {
				t.Fatalf("失败清理破坏了已提交值: %t %v", found, err)
			}
		})
	}
}

// TestFileConditionalClearRejectsCorruptAndUnscopedEntries 验证不能将损坏或未知键归属误判为可删除数据。
func TestFileConditionalClearRejectsCorruptAndUnscopedEntries(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		content string
		want    error
	}{
		{"corrupt", "not-json", ErrCorruptCacheEntry},
		{"unscoped", `{"value":"old"}`, ErrUnscopedCacheEntry},
		{"wrong identity", `{"key":"another","value":"old"}`, ErrUnscopedCacheEntry},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			backend, _ := newTestFileDriver(t)
			path := backend.cacheFilePath("scope:unknown")
			if err := os.WriteFile(path, []byte(fixture.content), 0o600); err != nil {
				t.Fatal(err)
			}
			err := backend.ClearPrefixIfContext(context.Background(), "scope:", nil, func(interface{}) (bool, error) { return true, nil })
			if !errors.Is(err, fixture.want) {
				t.Fatalf("错误文件归属未被拒绝: %v", err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("未知归属文件被删除: %v", err)
			}
		})
	}
	var missing *File
	if err := missing.ClearPrefixIfContext(context.Background(), "scope:", nil, nil); !errors.Is(err, ErrInvalidCachePath) {
		t.Fatalf("nil 文件驱动未被拒绝: %v", err)
	}
	backend := &File{path: filepath.Join(t.TempDir(), "missing")}
	if err := backend.ClearPrefixIfContext(context.Background(), "scope:", nil, func(interface{}) (bool, error) { return true, nil }); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺失存储目录错误丢失: %v", err)
	}
}

// TestFileUpdateContextCancelsWaitingGuard 验证文件后端在锁竞争时也遵守请求取消信号。
func TestFileUpdateContextCancelsWaitingGuard(t *testing.T) {
	first, directory := newTestFileDriver(t)
	second, err := NewFile(directory)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- first.withMutationGuard("blocked", func() error { close(started); <-release; return nil })
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	called := false
	err = second.UpdateContext(ctx, "blocked", 0, func(interface{}, bool) (interface{}, bool, error) { called = true; return "unexpected", false, nil })
	close(release)
	ownerErr := <-done
	if called || !errors.Is(err, context.DeadlineExceeded) || ownerErr != nil {
		t.Fatalf("锁等待取消失败: called=%t waiter=%v owner=%v", called, err, ownerErr)
	}
}
