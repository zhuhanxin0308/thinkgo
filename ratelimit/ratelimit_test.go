package ratelimit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestMemoryStoreImplementsGCRA 验证突发容量、稳定速率、剩余额度和重置时间。
func TestMemoryStoreImplementsGCRA(t *testing.T) {
	store, err := NewMemoryStore(10)
	if err != nil {
		t.Fatalf("创建内存限流存储失败: %v", err)
	}
	limit := Limit{Rate: 2, Period: time.Second, Burst: 3}
	now := time.Unix(100, 0)
	for expectedRemaining := 2; expectedRemaining >= 0; expectedRemaining-- {
		result, takeErr := store.Take(context.Background(), "client", limit, now)
		if takeErr != nil || !result.Allowed || result.Remaining != expectedRemaining {
			t.Fatalf("突发请求结果错误: remaining=%d result=%#v err=%v", expectedRemaining, result, takeErr)
		}
	}
	denied, err := store.Take(context.Background(), "client", limit, now)
	if err != nil || denied.Allowed || denied.RetryAfter != 500*time.Millisecond {
		t.Fatalf("超额请求结果错误: result=%#v err=%v", denied, err)
	}
	allowed, err := store.Take(context.Background(), "client", limit, now.Add(500*time.Millisecond))
	if err != nil || !allowed.Allowed || allowed.Remaining != 0 {
		t.Fatalf("恢复一个额度后的结果错误: result=%#v err=%v", allowed, err)
	}
}

// TestMemoryStoreBoundsUntrustedKeys 验证键空间满时不会驱逐活跃键并放大攻击者额度。
func TestMemoryStoreBoundsUntrustedKeys(t *testing.T) {
	store, _ := NewMemoryStore(1)
	limit := Limit{Rate: 1, Period: time.Minute, Burst: 1}
	now := time.Unix(200, 0)
	if _, err := store.Take(context.Background(), "first", limit, now); err != nil {
		t.Fatalf("首个键限流失败: %v", err)
	}
	if _, err := store.Take(context.Background(), "second", limit, now); !errors.Is(err, ErrStoreCapacity) {
		t.Fatalf("活跃键占满时应返回容量错误，实际为 %v", err)
	}
	if result, err := store.Take(context.Background(), "second", limit, now.Add(time.Minute)); err != nil || !result.Allowed {
		t.Fatalf("旧键自然过期后应接纳新键: result=%#v err=%v", result, err)
	}
}

// TestMemoryStoreValidatesInputsAndCancellation 验证配置、键和请求上下文边界。
func TestMemoryStoreValidatesInputsAndCancellation(t *testing.T) {
	if _, err := NewMemoryStore(0); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("非法容量应返回配置错误，实际为 %v", err)
	}
	store, _ := NewMemoryStore(1)
	invalidLimits := []Limit{
		{},
		{Rate: 1, Period: 0, Burst: 1},
		{Rate: 1_000_000_001, Period: time.Second, Burst: 1},
		{Rate: 1, Period: time.Second, Burst: 0},
	}
	for _, limit := range invalidLimits {
		if _, err := store.Take(context.Background(), "key", limit, time.Now()); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("非法限流配置未被拒绝: limit=%#v err=%v", limit, err)
		}
	}
	if _, err := store.Take(context.Background(), "", Limit{Rate: 1, Period: time.Second, Burst: 1}, time.Now()); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("空键应返回键错误，实际为 %v", err)
	}
	oversizedWhitespace := strings.Repeat(" ", maximumKeyBytes) + "key"
	if _, err := store.Take(context.Background(), oversizedWhitespace, Limit{Rate: 1, Period: time.Second, Burst: 1}, time.Now()); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("裁剪前超长的键应被拒绝，实际为 %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Take(cancelled, "key", Limit{Rate: 1, Period: time.Second, Burst: 1}, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消请求应保留上下文错误，实际为 %v", err)
	}
}
