package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestIssue131RedisNestedNamespacesDoNotShareOrResetQuota(t *testing.T) {
	server := miniredis.RunT(t)
	// Keep the server clock fixed so a quota cannot refill during the assertions.
	server.SetTime(time.Unix(1_700_000_000, 0))
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	short, err := NewRedisStore(client, RedisStoreOptions{Prefix: "tenant:"})
	if err != nil {
		t.Fatal(err)
	}
	long, err := NewRedisStore(client, RedisStoreOptions{Prefix: "tenant:vip:"})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := NewRedisStore(client, RedisStoreOptions{Prefix: "tenant:vip:"})
	if err != nil {
		t.Fatal(err)
	}
	limit := Limit{Rate: 1, Period: time.Minute, Burst: 1}
	ctx := context.Background()
	if result, err := long.Take(ctx, "client", limit, time.Time{}); err != nil || !result.Allowed {
		t.Fatalf("initial VIP quota: %+v %v", result, err)
	}
	if result, err := short.Take(ctx, "vip:client", limit, time.Time{}); err != nil || !result.Allowed {
		t.Fatalf("short prefix consumed VIP quota: %+v %v", result, err)
	}
	if err := short.Reset(ctx, "vip:client"); err != nil {
		t.Fatal(err)
	}
	if result, err := peer.Take(ctx, "client", limit, time.Time{}); err != nil || result.Allowed {
		t.Fatalf("short prefix reset VIP quota: %+v %v", result, err)
	}
	if result, err := short.Take(ctx, "vip:client", limit, time.Time{}); err != nil || !result.Allowed {
		t.Fatalf("reset did not restore its own quota: %+v %v", result, err)
	}
	if err := peer.Reset(ctx, "client"); err != nil {
		t.Fatal(err)
	}
	if result, err := long.Take(ctx, "client", limit, time.Time{}); err != nil || !result.Allowed {
		t.Fatalf("same-prefix instances no longer share quota: %+v %v", result, err)
	}
	if err := server.Set("tenant:vip:client", "ambiguous-legacy-state"); err != nil {
		t.Fatal(err)
	}
	if err := short.Reset(ctx, "vip:client"); err != nil {
		t.Fatal(err)
	}
	if value, err := server.Get("tenant:vip:client"); err != nil || value != "ambiguous-legacy-state" {
		t.Fatalf("reset deleted an ambiguous legacy key: %q %v", value, err)
	}
}
