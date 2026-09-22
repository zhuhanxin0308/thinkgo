//go:build integration

package redis

import "testing"

// TestLiveRedisAtomicTTLPolicy 验证实际 Redis WATCH/MULTI 内的 TTL 决策及冲突回滚。
func TestLiveRedisAtomicTTLPolicy(t *testing.T) {
	testRedisAtomicTTLPolicy(t, connectLiveRedis(t))
}
