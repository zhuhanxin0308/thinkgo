package redis

import (
	"errors"
	"testing"
	"time"
)

// 原始前缀是现有公开配置。owner 可以是任意非空文本，包括一个完全合法的 JSON 值。
func TestAncestorClearMustPreserveLegacyRawPrefixLease(t *testing.T) {
	for _, sweep := range []string{"all", "prefix", "conditional"} {
		t.Run(sweep, func(t *testing.T) {
			parent := newConditionalRedis(t)
			child := &Redis{client: parent.client, prefix: parent.prefix + "tenant-a:", opTimeout: parent.opTimeout}
			const lock = managerLockKeyPrefix + "intro"
			const owner = `{"value":"业务数据"}`
			if acquired, err := child.AcquireLock(lock, owner, time.Minute); err != nil || !acquired {
				t.Fatalf("准备活动锁失败：%t %v", acquired, err)
			}
			var err error
			switch sweep {
			case "all":
				err = parent.Clear()
			case "prefix":
				err = parent.ClearPrefix("tenant-a:")
			case "conditional":
				err = parent.ClearPrefixIfContext(t.Context(), "tenant-a:", nil, func(interface{}) (bool, error) {
					return true, nil
				})
			}
			if !errors.Is(err, ErrAmbiguousRedisClearScope) || !errors.Is(err, ErrUnsafeRedisFlush) {
				t.Fatalf("归属不确定的物理键不能报告清理成功：%v", err)
			}
			if second, err := child.AcquireLock(lock, "other", time.Minute); err != nil || second {
				t.Fatalf("上层清理删除了有效租约：second=%t err=%v", second, err)
			}
			if released, err := child.ReleaseLock(lock, owner); err != nil || !released {
				t.Fatalf("原 owner 无法释放租约：%t %v", released, err)
			}
		})
	}
}
