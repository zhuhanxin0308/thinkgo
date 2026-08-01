package driver

import (
	"errors"
	"testing"
	"time"
)

// TestNewRedisStrictConfig 验证 Redis Session 在启动阶段拒绝缺少命名空间和危险配置。
func TestNewRedisStrictConfig(t *testing.T) {
	valid := map[string]interface{}{
		"host": "127.0.0.1", "port": 6379, "prefix": "thinkgo:test:session:",
	}
	driver, err := NewRedis(valid)
	if err != nil || driver == nil {
		t.Fatalf("合法 Redis Session 配置创建失败: driver=%v err=%v", driver, err)
	}
	if err = driver.Close(); err != nil {
		t.Fatalf("关闭 Redis Session 客户端失败: %v", err)
	}

	invalid := []map[string]interface{}{
		{},
		{"prefix": ""},
		{"prefix": "unsafe:*"},
		{"prefix": "thinkgo:test:", "port": 0},
		{"prefix": "thinkgo:test:", "timeout_ms": 0},
		{"prefix": "thinkgo:test:", "tls_server_name": "redis.example.com"},
		{"prefix": "thinkgo:test:", "password": true},
		{"prefix": "thinkgo:test:", "tls_server_name": true, "tls_enable": true},
		{"prefix": "thinkgo:test:", "unknown": true},
	}
	for _, config := range invalid {
		if instance, createErr := NewRedis(config); !errors.Is(createErr, ErrInvalidRedisSessionConfig) || instance != nil {
			t.Fatalf("非法 Redis Session 配置应失败: config=%#v instance=%v err=%v", config, instance, createErr)
		}
	}
}

// TestRedisSessionTTL 验证 Redis Session 从标准信封正确推导原生 TTL。
func TestRedisSessionTTL(t *testing.T) {
	if ttl, err := redisSessionTTL(`{"version":1,"data":{}}`); err != nil || ttl != 0 {
		t.Fatalf("无过期时间的 Session TTL 错误: ttl=%v err=%v", ttl, err)
	}
	if ttl, err := redisSessionTTL(`{"expire_at":1}`); err != nil || ttl <= 0 || ttl > time.Millisecond*2 {
		t.Fatalf("已过期 Session TTL 应被压缩为最小值: ttl=%v err=%v", ttl, err)
	}
	if _, err := redisSessionTTL(`not-json`); err == nil {
		t.Fatal("非法 Session 信封必须拒绝推导 TTL")
	}
}
