package redis

import (
	"context"
	"errors"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/cachekey"
)

// ErrAmbiguousRedisClearScope 表示扫描范围包含无法确认归属的锁或 fencing
// 元数据候选键。调用方必须缩小范围或隔离数据库，不能把部分清理当作成功。
var ErrAmbiguousRedisClearScope = errors.New("Redis 清理范围包含归属不明确的保护元数据")

// redisClearKeyEligible 按精确保留前缀与 canonical namespace 帧边界识别内部锁和
// fencing 元数据键；普通业务键中间包含保留标记文本不会阻塞清理，只有损坏或未知版本的
// __thinkgo_namespace__: 帧携带保护标记时才失败关闭。
func redisClearKeyEligible(key, driverPrefix, scopePrefix string) (bool, error) {
	if !strings.HasPrefix(key, driverPrefix) || !strings.HasPrefix(key, scopePrefix) {
		return false, nil
	}
	for _, prefix := range []string{driverPrefix, scopePrefix} {
		logical := strings.TrimPrefix(key, prefix)
		protected, ambiguous := cachekey.ClassifyProtectedMetadata(logical)
		if ambiguous {
			return false, errors.Join(ErrUnsafeRedisFlush, ErrAmbiguousRedisClearScope)
		}
		if protected {
			return false, nil
		}
	}
	return true, nil
}

// clearPhysicalPrefixContext 逐批校验再删除，不修改锁布局或现有 owner 协议。
// SCAN 清理本来就不是全库事务；后续批次失败时，之前删除的数据不会回滚。
func (c *Redis) clearPhysicalPrefixContext(parent context.Context, prefix string) error {
	ctx, cancel := context.WithTimeout(parent, c.opTimeout*5)
	defer cancel()
	var cursor uint64
	for {
		keys, next, err := c.client.Scan(ctx, cursor, redisScanPattern(prefix), 256).Result()
		if err != nil {
			return err
		}
		deletable := keys[:0]
		for _, key := range keys {
			eligible, err := redisClearKeyEligible(key, c.prefix, prefix)
			if err != nil {
				return err
			}
			if eligible {
				deletable = append(deletable, key)
			}
		}
		if len(deletable) > 0 {
			if err := c.client.Del(ctx, deletable...).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}
