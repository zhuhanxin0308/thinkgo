package redis

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cache/contract"
)

func TestFenceAllocationPreservesLegacySequenceAndExactIntegers(t *testing.T) {
	c := newConditionalRedis(t)
	ctx := t.Context()
	const sequence, issued = "sequence", "issued"
	const high = uint64(1<<53 + 7)
	// 旧版创建的原生 Redis 整数在升级后继续递增，并覆盖十进制尾数进位。
	if err := c.client.Set(ctx, c.withPrefix(sequence), strconv.FormatUint(high, 10), 0).Err(); err != nil {
		t.Fatal(err)
	}
	next := func(floor, want uint64) {
		t.Helper()
		if got, err := c.AllocateFenceGenerationContext(ctx, sequence, issued, floor, 0); got != want || err != nil {
			t.Fatalf("票号错误：floor=%d want=%d got=%d err=%v", floor, want, got, err)
		}
		for _, key := range []string{sequence, issued} {
			if value, err := c.client.Get(ctx, c.withPrefix(key)).Uint64(); err != nil || value != want {
				t.Fatalf("物理记录不一致：key=%s value=%d err=%v", key, value, err)
			}
		}
	}
	next(0, high+1)
	if err := c.Delete(sequence); err != nil {
		t.Fatal(err)
	}
	next(0, high+2)
	if err := c.Delete(issued); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Inc(sequence, 3); err != nil {
		t.Fatal(err)
	}
	next(0, high+6)
	next(math.MaxInt64-1, math.MaxInt64)
	if _, err := c.AllocateFenceGenerationContext(ctx, sequence, issued, 0, 0); !errors.Is(err, contract.ErrCacheFenceUnavailable) {
		t.Fatalf("耗尽后仍分配票号：%v", err)
	}
}

func TestFenceAllocationRejectsInvalidWitnessBeforeMutation(t *testing.T) {
	c := newConditionalRedis(t)
	ctx := t.Context()
	const sequence, issued = "sequence", "issued"
	for _, invalid := range []string{"001", "-3", "1.5", "broken", "9223372036854775808"} {
		if err := c.client.MSet(ctx, c.withPrefix(sequence), "41", c.withPrefix(issued), invalid).Err(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.AllocateFenceGenerationContext(ctx, sequence, issued, 0, 0); !errors.Is(err, contract.ErrCacheFenceUnavailable) {
			t.Fatalf("应拒绝损坏的记录 %q：%v", invalid, err)
		}
		if got, err := c.client.Get(ctx, c.withPrefix(sequence)).Result(); err != nil || got != "41" {
			t.Fatalf("错误覆盖了原序列：got=%s err=%v", got, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.AllocateFenceGenerationContext(cancelled, sequence, issued, 0, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("忽略请求取消：%v", err)
	}
}
