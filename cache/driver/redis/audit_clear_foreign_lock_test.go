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

func TestAuditClearMustPreserveNamespacedActiveLocks(t *testing.T) {
	cases := []struct {
		name, sweepPrefix, logicalPrefix, businessPrefix string
		allowFlush, wantAmbiguous                        bool
	}{
		{"unprefixed", "", "", "tenant-a:", true, true},
		{"ancestor", "shared:", "", "shared:tenant-a:", false, true},
		{"own", "tenant-a:", "", "tenant-a:", false, false},
		{"explicit_namespace", "", "tenant-a:", "tenant-a:", false, false},
		{"nested_ancestor", "", "shared:", "shared:tenant-a:", false, true},
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
				if conditional {
					err = sweep.ClearPrefixIfContext(context.Background(), tc.logicalPrefix, nil,
						func(interface{}) (bool, error) { return true, nil })
				} else if tc.logicalPrefix != "" {
					err = sweep.ClearPrefix(tc.logicalPrefix)
				} else {
					err = sweep.Clear()
				}
				if tc.wantAmbiguous {
					if !errors.Is(err, ErrAmbiguousRedisClearScope) || !errors.Is(err, ErrUnsafeRedisFlush) {
						t.Fatalf("expected explicit unsafe-scope error, got %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if owned, err := business.AcquireLock(lock, "owner-B", time.Minute); err != nil || owned {
					t.Fatalf("active lock lost: second owner=%t error=%v", owned, err)
				}
				if found, err := business.Has(cacheFenceMetadataPrefix + "test"); err != nil || !found {
					t.Fatalf("fencing metadata lost: %t %v", found, err)
				}
				if !tc.wantAmbiguous {
					if found, err := business.Has("ordinary"); err != nil || found {
						t.Fatalf("ordinary data not cleared: %t %v", found, err)
					}
				}
			})
		}
	}
}

func TestRedisClearScopeDoesNotSilentlySkipAmbiguousBusinessKeys(t *testing.T) {
	key := "app:report__thinkgo_lock__:balance"
	if eligible, err := redisClearKeyEligible(key, "app:", "app:"); eligible || !errors.Is(err, ErrAmbiguousRedisClearScope) {
		t.Fatalf("ambiguous business key must report failure, not silent success: %t %v", eligible, err)
	}
	if eligible, err := redisClearKeyEligible("other:data", "app:", "app:"); eligible || err != nil {
		t.Fatalf("out-of-scope key: %t %v", eligible, err)
	}
}
