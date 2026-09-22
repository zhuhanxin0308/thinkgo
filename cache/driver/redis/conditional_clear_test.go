package redis

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func newConditionalRedis(t *testing.T) *Redis {
	t.Helper()
	server := miniredis.RunT(t)
	host, portText, err := net.SplitHostPort(server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewRedis(map[string]interface{}{"host": host, "port": port, "prefix": "conditions:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

// TestRedisConditionalClearRechecksConcurrentWriter 验证 WATCH 冲突重试后不会按旧值条件删除新提交。
func TestRedisConditionalClearRechecksConcurrentWriter(t *testing.T) {
	testConditionalClearRechecksConcurrentWriter(t, newConditionalRedis(t))
}

func testConditionalClearRechecksConcurrentWriter(t *testing.T, backend *Redis) {
	t.Helper()
	if err := backend.Set("business:old", "old", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := backend.Set("business:keep", "keep", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := backend.Set(cacheFenceMetadataPrefix+"sequence", "10", 0); err != nil {
		t.Fatal(err)
	}
	if acquired, err := backend.AcquireLock(managerLockKeyPrefix+"held", "owner", time.Minute); err != nil || !acquired {
		t.Fatalf("准备活动锁失败: %t %v", acquired, err)
	}
	updated := false
	err := backend.ClearPrefixIfContext(context.Background(), "", func(key string) bool { return key != "business:keep" }, func(value interface{}) (bool, error) {
		if value == "old" && !updated {
			updated = true
			if err := backend.Set("business:old", "new", time.Minute); err != nil {
				return false, err
			}
		}
		return value == "old", nil
	})
	if err != nil || !updated {
		t.Fatalf("条件清理没有完成冲突重试: %t %v", updated, err)
	}
	for key, want := range map[string]string{"business:old": "new", "business:keep": "keep", cacheFenceMetadataPrefix + "sequence": "10"} {
		if value, found, err := backend.Get(key); err != nil || !found || value != want {
			t.Fatalf("清理破坏保留项: %s %v %t %v", key, value, found, err)
		}
	}
	if released, err := backend.ReleaseLock(managerLockKeyPrefix+"held", "owner"); err != nil || !released {
		t.Fatalf("清理破坏活动锁: %t %v", released, err)
	}
	if err := backend.ClearPrefixIfContext(context.Background(), "business:", nil, func(interface{}) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if _, found, err := backend.Get("business:old"); err != nil || found {
		t.Fatalf("应删除项仍存在: %t %v", found, err)
	}
}

// TestRedisConditionalClearFailureBoundaries 验证权限配置、取消、回调与连接错误都不会伪装为清理成功。
func TestRedisConditionalClearFailureBoundaries(t *testing.T) {
	backend := newConditionalRedis(t)
	remove := func(interface{}) (bool, error) { return true, nil }
	var nilContext context.Context
	if err := backend.ClearPrefixIfContext(nilContext, "prefix:", nil, remove); !errors.Is(err, ErrInvalidRedisContext) {
		t.Fatal(err)
	}
	if err := backend.ClearPrefixIfContext(context.Background(), "prefix:", nil, nil); !errors.Is(err, ErrNilAtomicUpdate) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := backend.ClearPrefixIfContext(ctx, "prefix:", nil, remove); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未传播: %v", err)
	}
	if err := backend.Set("prefix:key", "value", 0); err != nil {
		t.Fatal(err)
	}
	abort := errors.New("拒绝清理")
	if err := backend.ClearPrefixIfContext(context.Background(), "prefix:", nil, func(interface{}) (bool, error) { return false, abort }); !errors.Is(err, abort) {
		t.Fatalf("回调错误未传播: %v", err)
	}
	backend.prefix = ""
	if err := backend.ClearPrefixIfContext(context.Background(), "", nil, remove); !errors.Is(err, ErrUnsafeRedisFlush) {
		t.Fatalf("未授权的全库清理未被拒绝: %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	if err := backend.ClearPrefixIfContext(context.Background(), "prefix:", nil, remove); err == nil {
		t.Fatal("连接关闭后仍报告清理成功")
	}
	var missing *Redis
	if err := missing.ClearPrefixIfContext(context.Background(), "prefix:", nil, remove); !errors.Is(err, ErrInvalidRedisClient) {
		t.Fatalf("缺失连接错误未传播: %v", err)
	}
}
