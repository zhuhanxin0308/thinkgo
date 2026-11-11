package redis

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRedisExpirySnapshotRetriesWithCurrentState 验证 WATCH 重试重新读取值及有效期，不使用失败尝试的快照。
func TestRedisExpirySnapshotRetriesWithCurrentState(t *testing.T) {
	backend := newConditionalRedis(t)
	const key = "snapshot"
	if err := backend.Set(key, "old", time.Minute); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var captured time.Time
	err := backend.UpdateWithExpiryContext(context.Background(), key, time.Hour, func(value interface{}, found bool, expiry time.Time) (interface{}, bool, *time.Time, error) {
		calls++
		if !found || expiry.IsZero() {
			t.Fatal("必须读取当前值与有效期")
		}
		if calls == 1 {
			if err := backend.Set(key, "concurrent", 2*time.Minute); err != nil {
				t.Fatal(err)
			}
		} else if value != "concurrent" {
			t.Fatalf("重试没有重新读取值: %v", value)
		}
		captured = expiry
		return "committed", false, nil, nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("冲突重试失败: calls=%d err=%v", calls, err)
	}
	err = backend.UpdateWithExpiryContext(context.Background(), key, 0, func(value interface{}, found bool, _ time.Time) (interface{}, bool, *time.Time, error) {
		if !found || value != "committed" {
			t.Fatalf("恢复读取错误: value=%v found=%v", value, found)
		}
		return "restored", false, &captured, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if value, found, err := backend.Get(key); err != nil || !found || value != "restored" {
		t.Fatalf("恢复失败: value=%v found=%v err=%v", value, found, err)
	}
	if ttl, err := backend.client.PTTL(context.Background(), backend.withPrefix(key)).Result(); err != nil || ttl < time.Minute || ttl > 2*time.Minute {
		t.Fatalf("恢复没有使用第二次读取的有效期: ttl=%v err=%v", ttl, err)
	}
	permanent := time.Time{}
	if err := backend.UpdateWithExpiryContext(context.Background(), key, time.Hour, func(interface{}, bool, time.Time) (interface{}, bool, *time.Time, error) {
		return "permanent", false, &permanent, nil
	}); err != nil {
		t.Fatal(err)
	}
	if ttl := backend.client.PTTL(context.Background(), backend.withPrefix(key)).Val(); ttl != redisExpiryPermanent {
		t.Fatalf("永久项被错误加上 TTL: %v", ttl)
	}
	past := time.Unix(1, 0)
	if err := backend.UpdateWithExpiryContext(context.Background(), key, 0, func(interface{}, bool, time.Time) (interface{}, bool, *time.Time, error) {
		return "expired", false, &past, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := backend.Get(key); err != nil || found {
		t.Fatalf("已到期快照被复活: found=%v err=%v", found, err)
	}
}

// TestRedisExpirySnapshotRejectsInvalidCalls 验证无效输入和取消在提交前失败。
func TestRedisExpirySnapshotRejectsInvalidCalls(t *testing.T) {
	backend := newConditionalRedis(t)
	update := func(interface{}, bool, time.Time) (interface{}, bool, *time.Time, error) {
		return "value", false, nil, nil
	}
	var nilContext context.Context
	if err := backend.UpdateWithExpiryContext(nilContext, "key", 0, update); !errors.Is(err, ErrInvalidRedisContext) {
		t.Fatal(err)
	}
	if err := backend.UpdateWithExpiryContext(context.Background(), "key", 0, nil); !errors.Is(err, ErrNilAtomicUpdate) {
		t.Fatal(err)
	}
	if err := backend.UpdateWithExpiryContext(context.Background(), "key", -time.Second, update); !errors.Is(err, ErrInvalidDriverTTL) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := backend.UpdateWithExpiryContext(ctx, "key", 0, update); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
