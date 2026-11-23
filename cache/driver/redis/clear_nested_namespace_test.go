package redis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/cachekey"
)

func TestRedisClearPreservesNestedNamespaceLocks(t *testing.T) {
	testRedisClearPreservesNestedNamespaceLocks(t, newConditionalRedis(t))
}

// 用完整命名空间驱动写入业务数据和锁，验证三种公开清理入口保持原有清理范围及锁所有权。
func testRedisClearPreservesNestedNamespaceLocks(t *testing.T, backend *Redis) {
	t.Helper()
	parent, err := cache.NewNamespaceDriver(backend, "商店:东区", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := cache.NewNamespaceDriver(parent, "仓库:甲", "")
	if err != nil {
		t.Fatal(err)
	}
	const lockKey = managerLockKeyPrefix + "发货"
	for _, tt := range []struct {
		name        string
		clear       func() error
		keepOutside bool
	}{
		{"Clear", backend.Clear, false},
		{"ClearPrefix", parent.Clear, true},
		{"ClearPrefixIfContext", func() error {
			return parent.ClearPrefixIfContext(context.Background(), "", nil, func(any) (bool, error) { return true, nil })
		}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := backend.Set("outside", "外层", 0); err != nil {
				t.Fatal(err)
			}
			if err := child.Set("订单", "待处理", 0); err != nil {
				t.Fatal(err)
			}
			if held, err := child.AcquireLock(lockKey, "原持有者", time.Minute); err != nil || !held {
				t.Fatalf("取得子命名空间锁失败：held=%t err=%v", held, err)
			}
			t.Cleanup(func() {
				if released, err := child.ReleaseLock(lockKey, "原持有者"); err != nil || !released {
					t.Errorf("原持有者无法释放锁：released=%t err=%v", released, err)
				}
			})
			if err := tt.clear(); err != nil {
				t.Fatal(err)
			}
			if held, err := child.AcquireLock(lockKey, "竞争者", time.Minute); err != nil || held {
				t.Fatalf("清理后活动锁失去互斥：held=%t err=%v", held, err)
			}
			if found, err := child.Has("订单"); err != nil || found {
				t.Fatalf("子命名空间业务值未清理：found=%t err=%v", found, err)
			}
			if found, err := backend.Has("outside"); err != nil || found != tt.keepOutside {
				t.Fatalf("外层业务值与预期清理范围不符：found=%t want=%t err=%v", found, tt.keepOutside, err)
			}
		})
	}
}

// 只有物理驱动前缀后的完整帧可用于证明保护键身份，其余情况仍按原始前缀冲突契约处理。
func TestRedisClearNamespaceFrameBoundaries(t *testing.T) {
	const driverPrefix = "宿主:" + managerLockKeyPrefix
	framed := cachekey.Namespace("一级:中文") + cachekey.Namespace("二级:"+managerLockKeyPrefix)
	for _, tc := range []struct {
		name      string
		key       string
		eligible  bool
		ambiguous bool
	}{
		{"驱动前缀内标记", "业务", true, false},
		{"完整帧锁", framed + managerLockKeyPrefix + "订单", false, false},
		{"完整帧栅栏", framed + cacheFenceMetadataPrefix + "水位", false, false},
		{"锁后缀按原始字节保护", framed + managerLockKeyPrefix + "\n\xff", false, false},
		{"外帧含换行", cachekey.Namespace("东\n区") + framed + managerLockKeyPrefix + "订单", false, true},
		{"内帧含控制字符", framed + cachekey.Namespace("东\u0085区") + cacheFenceMetadataPrefix + "水位", false, true},
		{"内帧含无效编码", framed + cachekey.Namespace("\xff") + managerLockKeyPrefix + "订单", false, true},
		{"仅帧内容含标记", framed + "业务", false, true},
		{"普通键内标记", "doc:" + managerLockKeyPrefix + "intro", false, true},
		{"外部旧前缀的命名空间锁", "外部:" + framed + managerLockKeyPrefix + "订单", false, true},
		{"错误字节长度", cachekey.NamespacePrefix + "v2:1:甲:" + managerLockKeyPrefix + "订单", false, true},
		{"过长帧", cachekey.Namespace(strings.Repeat("x", cachekey.MaxLogicalKeyBytes+1)) + managerLockKeyPrefix + "订单", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eligible, err := redisClearKeyEligible(driverPrefix+tc.key, driverPrefix, driverPrefix)
			if tc.ambiguous {
				if eligible || !errors.Is(err, ErrAmbiguousRedisClearScope) || !errors.Is(err, ErrUnsafeRedisFlush) {
					t.Fatalf("归属不明时应拒绝清理：eligible=%t err=%v", eligible, err)
				}
				return
			}
			if eligible != tc.eligible || err != nil {
				t.Fatalf("清理判定不符：eligible=%t err=%v", eligible, err)
			}
		})
	}
}
