package redis

import (
	"context"
	"errors"
	"testing"
	"time"
)

type conditionalTTLUpdater interface {
	UpdatePreserveTTLConditionally(string, func(interface{}, bool) (interface{}, bool, bool, error)) error
}

// TestRedisAtomicTTLPolicy 在 WATCH 冲突后重新决定 TTL，不能沿用未提交尝试的选择。
func TestRedisAtomicTTLPolicy(t *testing.T) {
	testRedisAtomicTTLPolicy(t, newConditionalRedis(t))
}

func testRedisAtomicTTLPolicy(t *testing.T, backend *Redis) {
	t.Helper()
	updater, ok := interface{}(backend).(conditionalTTLUpdater)
	if !ok {
		t.Fatal("Redis 缺少原子 TTL 决策能力")
	}
	const key = "ttl-policy"
	for _, preserve := range []bool{true, false} {
		if err := backend.Set(key, "old", time.Minute); err != nil {
			t.Fatal(err)
		}
		calls := 0
		err := updater.UpdatePreserveTTLConditionally(key, func(value interface{}, found bool) (interface{}, bool, bool, error) {
			calls++
			if !found {
				return nil, false, false, errors.New("WATCH 未读取现存项")
			}
			if calls == 1 {
				if err := backend.Set(key, "concurrent", 2*time.Minute); err != nil {
					return nil, false, false, err
				}
				return "uncommitted", false, !preserve, nil
			}
			if value != "concurrent" {
				return nil, false, false, errors.New("WATCH 重试没有读取并发新值")
			}
			return "committed", false, preserve, nil
		})
		if err != nil || calls != 2 {
			t.Fatalf("WATCH 冲突未正确重试: calls=%d err=%v", calls, err)
		}
		ttl, err := backend.client.PTTL(context.Background(), backend.withPrefix(key)).Result()
		if err != nil || preserve && (ttl <= time.Minute || ttl > 2*time.Minute) || !preserve && ttl != -1 {
			t.Fatalf("最终尝试的 TTL 决策未提交: preserve=%t ttl=%s err=%v", preserve, ttl, err)
		}
		failure := errors.New("业务拒绝提交")
		if err := updater.UpdatePreserveTTLConditionally(key, func(interface{}, bool) (interface{}, bool, bool, error) {
			return "wrong", true, false, failure
		}); !errors.Is(err, failure) {
			t.Fatalf("回调错误未传播: %v", err)
		}
		after, err := backend.client.PTTL(context.Background(), backend.withPrefix(key)).Result()
		if err != nil || preserve && (after <= 0 || after > ttl) || !preserve && after != -1 {
			t.Fatalf("失败改变了 TTL: %s %v", after, err)
		}
		if value, found, err := backend.Get(key); err != nil || !found || value != "committed" {
			t.Fatalf("失败改变了值: %v %t %v", value, found, err)
		}
	}
	if err := updater.UpdatePreserveTTLConditionally(key, nil); !errors.Is(err, ErrNilAtomicUpdate) {
		t.Fatalf("空回调没有失败关闭: %v", err)
	}
	if err := updater.UpdatePreserveTTLConditionally(key, func(interface{}, bool) (interface{}, bool, bool, error) {
		return nil, true, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := backend.Get(key); err != nil || found {
		t.Fatalf("删除没有优先于 TTL 保留: %t %v", found, err)
	}
	if err := updater.UpdatePreserveTTLConditionally(key, func(_ interface{}, found bool) (interface{}, bool, bool, error) {
		if found {
			t.Fatal("删除后的键不应存在")
		}
		return "created", false, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if ttl, err := backend.client.PTTL(context.Background(), backend.withPrefix(key)).Result(); err != nil || ttl != -1 {
		t.Fatalf("缺失项创建必须永久有效: %s %v", ttl, err)
	}
}
