package redis

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/cachekey"
)

func TestAuditClearMustPreserveNamespacedActiveLocks(t *testing.T) {
	framedTenant := cachekey.Namespace("tenant-a:")
	framedNested := cachekey.Namespace("shared:") + cachekey.Namespace("tenant-a:")
	cases := []struct {
		name, sweepPrefix, logicalPrefix, businessPrefix string
		allowFlush                                       bool
	}{
		{"unprefixed", "", "", framedTenant, true},
		{"ancestor", cachekey.Namespace("shared:"), "", framedNested, false},
		{"own", framedTenant, "", framedTenant, false},
		{"explicit_namespace", "", framedTenant, framedTenant, false},
		{"nested_ancestor", "", cachekey.Namespace("shared:"), framedNested, false},
	}
	for _, tc := range cases {
		for _, conditional := range []bool{false, true} {
			t.Run(tc.name+"/conditional="+strconv.FormatBool(conditional), func(t *testing.T) {
				server := miniredis.RunT(t)
				host, portText, err := net.SplitHostPort(server.Addr())
				if err != nil {
					t.Fatal(err)
				}
				port, err := strconv.Atoi(portText)
				if err != nil {
					t.Fatal(err)
				}
				create := func(prefix string, allow bool) *Redis {
					t.Helper()
					driver, err := NewRedis(map[string]interface{}{
						"host": host, "port": port, "prefix": prefix, "allow_flush_db": allow,
					})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := driver.Close(); err != nil {
							t.Error(err)
						}
					})
					return driver
				}
				business, sweep := create(tc.businessPrefix, false), create(tc.sweepPrefix, tc.allowFlush)
				const lock = "__thinkgo_lock__:critical"
				if owned, err := business.AcquireLock(lock, "owner-A", time.Minute); err != nil || !owned {
					t.Fatalf("initial lock: %t %v", owned, err)
				}
				if err := business.Set(cacheFenceMetadataPrefix+"test", 42, 0); err != nil {
					t.Fatal(err)
				}
				if err := business.Set("ordinary", "value", 0); err != nil {
					t.Fatal(err)
				}
				if err := business.Set("doc:__thinkgo_lock__:intro", "value", 0); err != nil {
					t.Fatal(err)
				}
				if conditional {
					err = sweep.ClearPrefixIfContext(context.Background(), tc.logicalPrefix, nil,
						func(interface{}) (bool, error) { return true, nil })
				} else if tc.logicalPrefix != "" {
					err = sweep.ClearPrefix(tc.logicalPrefix)
				} else {
					err = sweep.Clear()
				}
				if err != nil {
					t.Fatal(err)
				}
				if owned, err := business.AcquireLock(lock, "owner-B", time.Minute); err != nil || owned {
					t.Fatalf("active lock lost: second owner=%t error=%v", owned, err)
				}
				if found, err := business.Has(cacheFenceMetadataPrefix + "test"); err != nil || !found {
					t.Fatalf("fencing metadata lost: %t %v", found, err)
				}
				for _, ordinaryKey := range []string{"ordinary", "doc:__thinkgo_lock__:intro"} {
					if found, err := business.Has(ordinaryKey); err != nil || found {
						t.Fatalf("ordinary data %q not cleared: %t %v", ordinaryKey, found, err)
					}
				}
			})
		}
	}
}

func TestRedisClearScopeDistinguishesBusinessSubstringsFromMalformedFrames(t *testing.T) {
	for _, key := range []string{
		"app:report__thinkgo_lock__:balance",
		"app:doc:__thinkgo_cache_fence__:intro",
		"app:" + cachekey.Namespace("tenant:") + "doc:__thinkgo_lock__:intro",
	} {
		if eligible, err := redisClearKeyEligible(key, "app:", "app:"); !eligible || err != nil {
			t.Fatalf("valid business key containing marker must be eligible: key=%q eligible=%t err=%v", key, eligible, err)
		}
	}
	for _, malformed := range []string{
		"app:" + cachekey.NamespacePrefix + "v2:999:short:__thinkgo_lock__:critical",
		"app:" + cachekey.NamespacePrefix + "v3:0::__thinkgo_cache_fence__:sequence",
	} {
		if eligible, err := redisClearKeyEligible(malformed, "app:", "app:"); eligible || !errors.Is(err, ErrAmbiguousRedisClearScope) || !errors.Is(err, ErrUnsafeRedisFlush) {
			t.Fatalf("malformed namespace frame with protected marker must fail closed: key=%q eligible=%t err=%v", malformed, eligible, err)
		}
	}
	if eligible, err := redisClearKeyEligible("other:data", "app:", "app:"); eligible || err != nil {
		t.Fatalf("out-of-scope key: %t %v", eligible, err)
	}
}
