//go:build integration

package redis

import "testing"

// TestLiveRedisConditionalClear 验证真实 Redis 的 SCAN、WATCH 冲突重试和活动锁保留。
func TestLiveRedisConditionalClear(t *testing.T) {
	backend := connectLiveRedis(t)
	t.Cleanup(func() {
		if err := backend.Delete(cacheFenceMetadataPrefix + "sequence"); err != nil {
			t.Errorf("清理测试代际元数据失败: %v", err)
		}
	})
	testConditionalClearRechecksConcurrentWriter(t, backend)
}
