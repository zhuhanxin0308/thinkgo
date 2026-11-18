package ratelimit

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestRedisStoreLargePeriodBurstDoesNotOverflowResetAfter 验证大周期突发边界下
// ResetAfter/RetryAfter 不发生有符号 int64 负溢出，且超过 2^53 微秒时保持精确微秒运算。
func TestRedisStoreLargePeriodBurstDoesNotOverflowResetAfter(t *testing.T) {
	server := miniredis.RunT(t)
	fixedNow := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	server.SetTime(fixedNow)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store, err := NewRedisStore(client, RedisStoreOptions{Prefix: "overflow:"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	limit := Limit{Rate: 2, Period: time.Duration(math.MaxInt64), Burst: 2}
	if _, err := Validate(limit); err != nil {
		t.Fatalf("合法边界配置应通过 Validate: %v", err)
	}
	res1, err := store.Take(ctx, "k1", limit, fixedNow)
	if err != nil || !res1.Allowed || res1.Remaining != 1 || res1.ResetAfter <= 0 {
		t.Fatalf("第 1 次 Take 结果异常: res=%+v err=%v", res1, err)
	}
	res2, err := store.Take(ctx, "k1", limit, fixedNow)
	if err != nil || !res2.Allowed || res2.Remaining != 0 || res2.ResetAfter != time.Duration(math.MaxInt64) {
		t.Fatalf("第 2 次 Take ResetAfter 发生负溢出或未饱和: res=%+v err=%v", res2, err)
	}
	res3, err := store.Take(ctx, "k1", limit, fixedNow)
	if err != nil || res3.Allowed || res3.RetryAfter <= 0 || res3.ResetAfter != time.Duration(math.MaxInt64) {
		t.Fatalf("第 3 次拒绝判定结果异常: res=%+v err=%v", res3, err)
	}

	// 验证超过 Lua float64 精确整数上限 (2^53 - 1 微秒) 时仍保持 1 微秒精度。
	const beyondFloat53Micros int64 = (1 << 53) + 7
	precisePeriod := time.Duration(beyondFloat53Micros) * time.Microsecond
	preciseLimit := Limit{Rate: 1, Period: precisePeriod, Burst: 1}
	preciseRes, err := store.Take(ctx, "precise", preciseLimit, fixedNow)
	if err != nil || !preciseRes.Allowed || preciseRes.ResetAfter != precisePeriod {
		t.Fatalf("超过 2^53 微秒时丢失精度: got=%v want=%v err=%v", preciseRes.ResetAfter, precisePeriod, err)
	}

	// 验证旧版单整数微秒存储格式的滚动升级读取兼容性。
	legacyTAT := fixedNow.Add(10 * time.Second).UnixMicro()
	if err := server.Set(store.prefix+"legacy", strconv.FormatInt(legacyTAT, 10)); err != nil {
		t.Fatal(err)
	}
	legacyRes, err := store.Take(ctx, "legacy", Limit{Rate: 1, Period: 10 * time.Second, Burst: 1}, fixedNow)
	if err != nil || legacyRes.Allowed || legacyRes.RetryAfter <= 0 {
		t.Fatalf("旧版微秒 TAT 未被正确兼容解析: res=%+v err=%v", legacyRes, err)
	}

	// 验证微秒向上取整后 Burst * intervalMicros 越过 int64 时安全拒绝。
	if _, _, err := redisRateLimitDurations(time.Duration(math.MaxInt64), 1001); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("Burst*intervalMicros 溢出应返回 ErrInvalidConfiguration，实际为 %v", err)
	}
}

// TestMemoryStoreReclaimsExpiredKeysAfterWallClockRollback 验证系统墙钟发生 NTP 回拨（now < origin）后，
// 早于初始 origin 的相对时间推进依然能正常清理已过期键，不会永久冻结容量回收。
func TestMemoryStoreReclaimsExpiredKeysAfterWallClockRollback(t *testing.T) {
	mem, err := NewMemoryStore(2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mem.Close() })

	ctx := context.Background()
	t0 := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	short := Limit{Rate: 100, Period: time.Second, Burst: 1} // 10ms 恢复周期

	if res, err := mem.Take(ctx, "ip1", short, t0); err != nil || !res.Allowed {
		t.Fatalf("写入 ip1 失败: res=%+v err=%v", res, err)
	}
	if res, err := mem.Take(ctx, "ip2", short, t0.Add(-500*time.Millisecond)); err != nil || !res.Allowed {
		t.Fatalf("墙钟回拨后写入 ip2 失败: res=%+v err=%v", res, err)
	}
	// ip2 尚未过期（仅推进 5ms）时，容量满应拒绝新键。
	if _, err := mem.Take(ctx, "ip3", short, t0.Add(-495*time.Millisecond)); !errors.Is(err, ErrStoreCapacity) {
		t.Fatalf("ip2 未过期时应拒绝超容新键，实际为 %v", err)
	}
	// 推进至 t0-100ms（仍早于初始 origin 100ms，但 ip2 已过期 390ms），应成功回收 ip2 并接纳 ip3。
	res3, err := mem.Take(ctx, "ip3", short, t0.Add(-100*time.Millisecond))
	if err != nil || !res3.Allowed {
		t.Fatalf("墙钟回拨区间内过期清理停摆: res=%+v err=%v", res3, err)
	}
}
