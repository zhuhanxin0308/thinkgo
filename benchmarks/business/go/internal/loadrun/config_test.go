package loadrun

import (
	"context"
	"math"
	"net"
	"testing"
	"time"
)

// testConfig 为每项测试提供独立配置，避免默认值变化掩盖契约。
func testConfig() Config {
	configuration := DefaultConfig()
	configuration.URL = "http://127.0.0.1:8080"
	configuration.Secret = "test-only-secret-do-not-log"
	configuration.RunID = "unit-run"
	configuration.Duration = 50 * time.Millisecond
	configuration.Timeout = time.Second
	configuration.Concurrency = 2
	configuration.MaxInflight = 2
	return configuration
}

func TestConfigLoopbackBoundary(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1:8080", "http://localhost:8000/", "http://[::1]:8000"} {
		configuration := testConfig()
		configuration.URL = target
		if err := configuration.Validate(); err != nil {
			t.Errorf("本地目标被拒绝 %s: %v", target, err)
		}
	}
	for _, target := range []string{"https://127.0.0.1", "http://example.com", "http://192.168.1.1", "http://0.0.0.0", "http://127.0.0.1.evil.test", "http://user:secret@127.0.0.1", "http://127.0.0.1/path", "http://127.0.0.1?a=1", "http://127.0.0.1#x", "http://127.0.0.1:0", "http://127.0.0.1:70000"} {
		configuration := testConfig()
		configuration.URL = target
		if err := configuration.Validate(); err == nil {
			t.Errorf("危险或歧义目标未拒绝 %s", target)
		}
	}
}

func TestConfigRejectsInvalidBudgets(t *testing.T) {
	tests := []func(*Config){
		func(c *Config) { c.Scenario = "unknown" },
		func(c *Config) { c.Duration = 0 },
		func(c *Config) { c.Timeout = 0 },
		func(c *Config) { c.Rate = -1 },
		func(c *Config) { c.Rate = math.NaN() },
		func(c *Config) { c.Rate = math.Inf(1) },
		func(c *Config) { c.Rate = math.MaxFloat64 },
		func(c *Config) { c.Concurrency = 0 },
		func(c *Config) { c.MaxInflight = 0 },
		func(c *Config) { c.TenantCount = 0 },
		func(c *Config) { c.ProductsPerTenant = 100 },
		func(c *Config) { c.UsersPerTenant = 0 },
		func(c *Config) { c.TicketsPerTenant = 0 },
		func(c *Config) { c.Categories = 0 },
		func(c *Config) { c.InactiveProductModulus = 1 },
		func(c *Config) { c.HotProductsPerTenant = 0 },
		func(c *Config) { c.HotProductsPerTenant = c.ProductsPerTenant },
		func(c *Config) { c.CacheHitWeight = -1 },
		func(c *Config) { c.CacheHitWeight = percentageTotal + 1 },
		func(c *Config) { c.MixedWeights.Catalog = 0 },
		func(c *Config) { c.MixedWeights.Catalog = math.MaxInt },
		func(c *Config) { c.MixedWeights.Catalog++ },
		func(c *Config) { c.Secret = "" },
		func(c *Config) { c.RunID = "" },
		func(c *Config) { c.ErrorSampleLimit = -1 },
	}
	for index, change := range tests {
		configuration := testConfig()
		change(&configuration)
		if err := configuration.Validate(); err == nil {
			t.Errorf("无效配置 #%d 被接受", index)
		}
	}
}

func TestLoopbackDialRejectsNonlocalAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, address := range []string{"example.com:80", "192.168.1.1:80", "missing-port", "127.0.0.1:80", "localhost:80"} {
		connection, err := dialLoopback(ctx, "tcp", address)
		if connection != nil || err == nil {
			t.Errorf("非法或取消连接未失败: %s %v", address, err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	connection, err := dialLoopback(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
}
