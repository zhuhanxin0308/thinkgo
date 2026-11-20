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

// redisClearKeyEligible 只将驱动已配置的前缀及合法帧作为归属证据。调用方的过滤前缀
// 可以在一个普通业务键的任意位置结束，不能把它当成另一个 Redis 驱动的物理配置。
func redisClearKeyEligible(key, driverPrefix, scopePrefix string) (bool, error) {
	if !strings.HasPrefix(key, driverPrefix) || !strings.HasPrefix(key, scopePrefix) {
		return false, nil
	}
	if !strings.Contains(key, managerLockKeyPrefix) && !strings.Contains(key, cacheFenceMetadataPrefix) {
		return true, nil
	}
	if protected, _ := cachekey.ClassifyProtectedMetadata(strings.TrimPrefix(key, driverPrefix)); protected {
		return false, nil
	}
	// 旧驱动的原始前缀没有分隔符，长度最多为 maxRedisPrefixBytes。逐一验证可能的
	// 帧起点也涵盖「旧前缀 + 任意层 namespace + 锁」，且不依据 owner 或 TTL 猜测。
	for boundary := 0; boundary <= min(maxRedisPrefixBytes, len(key)); boundary++ {
		suffix := key[boundary:]
		if !strings.HasPrefix(suffix, managerLockKeyPrefix) &&
			!strings.HasPrefix(suffix, cacheFenceMetadataPrefix) &&
			!strings.HasPrefix(suffix, cachekey.NamespacePrefix) {
			continue
		}
		protected, ambiguous := cachekey.ClassifyProtectedMetadata(suffix)
		if protected || ambiguous {
			return false, errors.Join(ErrUnsafeRedisFlush, ErrAmbiguousRedisClearScope)
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
