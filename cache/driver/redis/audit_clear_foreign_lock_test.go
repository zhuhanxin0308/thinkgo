package redis

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
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
				const embedded = "doc:__thinkgo_lock__:intro"
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
				if err := business.Set(embedded, "value", 0); err != nil {
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
				for _, ordinaryKey := range []string{"ordinary", embedded} {
					if found, err := business.Has(ordinaryKey); err != nil || found {
						t.Fatalf("ordinary data %q not cleared: %t %v", ordinaryKey, found, err)
					}
				}
			})
		}
	}
}

func TestRedisClearScopeDistinguishesBusinessSubstringsFromMalformedFrames(t *testing.T) {
	// 这些是合法业务键。旧驱动还允许产生相同物理键的前缀配置，因此分别记录尚未满足的清理契约。
	for _, key := range []string{
		"app:report__thinkgo_lock__:balance",
		"app:doc:__thinkgo_cache_fence__:intro",
		"app:" + cachekey.Namespace("tenant:") + "doc:__thinkgo_lock__:intro",
	} {
		t.Run(key, func(t *testing.T) {
			if eligible, err := redisClearKeyEligible(key, "app:", "app:"); !eligible || err != nil {
				t.Fatalf("合法业务键应允许清理：key=%q eligible=%t err=%v", key, eligible, err)
			}
		})
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

func TestRedisClearRemovesUnambiguousEmbeddedMarkers(t *testing.T) {
	// 原始驱动前缀的字节上限是确定边界。超过它的标记不可能位于原始前缀结尾。
	long := strings.Repeat("x", maxRedisPrefixBytes+1)
	for _, conditional := range []bool{false, true} {
		t.Run("conditional="+strconv.FormatBool(conditional), func(t *testing.T) {
			driver := newConditionalRedis(t)
			for _, key := range []string{
				"doc:" + long + "__thinkgo_lock__:intro",
				"doc:" + long + "__thinkgo_cache_fence__:guide",
				cachekey.Namespace("tenant:") + "doc:" + long + "__thinkgo_lock__:intro",
			} {
				if err := driver.Set(key, "value", time.Minute); err != nil {
					t.Fatal(err)
				}
				if conditional {
					if err := driver.ClearPrefixIfContext(t.Context(), "", nil, func(any) (bool, error) { return true, nil }); err != nil {
						t.Fatal(err)
					}
				} else if err := driver.Clear(); err != nil {
					t.Fatal(err)
				}
				if found, err := driver.Has(key); found || err != nil {
					t.Fatalf("无歧义业务键未物理删除：key=%q found=%t err=%v", key, found, err)
				}
			}
		})
	}
}

// 通过公开接口写入业务值，分别检查 ClearPrefix 和 Clear 是否真的删除对应物理键。
func TestRedisClearShortBusinessKeysContainingReservedMarkers(t *testing.T) {
	for _, tc := range []struct{ key, prefix string }{
		{"doc:__thinkgo_lock__:intro", "doc:"},
		{"doc:__thinkgo_cache_fence__:guide", "doc:"},
		{"article:__thinkgo_lock__:v2", ""},
	} {
		t.Run(tc.key, func(t *testing.T) {
			driver := newConditionalRedis(t)
			if err := driver.Set(tc.key, "value", time.Minute); err != nil {
				t.Fatal(err)
			}
			var err error
			if tc.prefix == "" {
				err = driver.Clear()
			} else {
				err = driver.ClearPrefix(tc.prefix)
			}
			if err != nil {
				t.Errorf("业务键清理失败：%v", err)
			}
			if found, err := driver.Has(tc.key); err != nil || found {
				t.Errorf("业务键仍存在或无法核验：key=%q found=%t err=%v", tc.key, found, err)
			}
		})
	}
}
