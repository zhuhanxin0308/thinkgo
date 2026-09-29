package redis

import (
	"context"
	"errors"
	"strings"
)

// ErrAmbiguousRedisClearScope 表示扫描范围包含无法确认归属的锁或 fencing
// 元数据候选键。调用方必须缩小范围或隔离数据库，不能把部分清理当作成功。
var ErrAmbiguousRedisClearScope = errors.New("Redis 清理范围包含归属不明确的保护元数据")

// redisClearKeyEligible 不根据值猜测锁类型：已知命名空间的保留键直接保护；
// 其余包含保留标记的键存在业务键/外部命名空间歧义，失败关闭而非静默跳过。
// scopePrefix 可以是比 driverPrefix 更窄的显式业务命名空间。
func redisClearKeyEligible(key, driverPrefix, scopePrefix string) (bool, error) {
	if !strings.HasPrefix(key, driverPrefix) || !strings.HasPrefix(key, scopePrefix) {
		return false, nil
	}
	for _, prefix := range []string{driverPrefix, scopePrefix} {
		if strings.HasPrefix(key, prefix+managerLockKeyPrefix) ||
			strings.HasPrefix(key, prefix+cacheFenceMetadataPrefix) {
			return false, nil
		}
	}
	logical := strings.TrimPrefix(key, driverPrefix)
	if strings.Contains(logical, managerLockKeyPrefix) || strings.Contains(logical, cacheFenceMetadataPrefix) {
		return false, errors.Join(ErrUnsafeRedisFlush, ErrAmbiguousRedisClearScope)
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
