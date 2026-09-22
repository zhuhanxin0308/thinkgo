package redis

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// TestRedisAtomicUpdatePreservesTTL 验证原子更新保留已有有效期，缺失键按契约创建为永久项。
func TestRedisAtomicUpdatePreservesTTL(t *testing.T) {
	server := miniredis.RunT(t)
	host, portText, err := net.SplitHostPort(server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewRedis(map[string]interface{}{"host": host, "port": port, "prefix": "atomic:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	if err := driver.Set("ttl", 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	physicalKey := driver.withPrefix("ttl")
	before, err := driver.client.PTTL(context.Background(), physicalKey).Result()
	if err != nil || before <= 0 {
		t.Fatalf("读取更新前 TTL 失败: ttl=%s err=%v", before, err)
	}
	if err = driver.UpdatePreserveTTL("ttl", func(value interface{}, found bool) (interface{}, bool, error) {
		if !found || value != float64(1) {
			return nil, false, errors.New("未读取原值")
		}
		return 2, false, nil
	}); err != nil {
		t.Fatal(err)
	}
	after, err := driver.client.PTTL(context.Background(), physicalKey).Result()
	if err != nil || after <= 0 || after > before {
		t.Fatalf("原子更新改变了 TTL: before=%s after=%s err=%v", before, after, err)
	}
	if err = driver.UpdatePreserveTTL("missing", func(interface{}, bool) (interface{}, bool, error) { return 1, false, nil }); err != nil {
		t.Fatal(err)
	}
	if ttl, ttlErr := driver.client.PTTL(context.Background(), driver.withPrefix("missing")).Result(); ttlErr != nil || ttl != -1 {
		t.Fatalf("缺失键应创建为永久项: ttl=%s err=%v", ttl, ttlErr)
	}
}
