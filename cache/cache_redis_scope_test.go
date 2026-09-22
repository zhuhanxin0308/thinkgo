package cache

import (
	"errors"
	"net"
	"strconv"
	"testing"

	"github.com/alicebob/miniredis/v2"
	redisDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver/redis"
)

// TestRejectedRedisFlushKeepsExistingValues 验证未授权的全库清理在发布失效水位之前失败。
func TestRejectedRedisFlushKeepsExistingValues(t *testing.T) {
	server := miniredis.RunT(t)
	host, portText, err := net.SplitHostPort(server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := redisDriver.NewRedis(map[string]interface{}{"host": host, "port": port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	for _, namespaced := range []bool{false, true} {
		var driver Driver = backend
		if namespaced {
			driver, err = NewNamespaceDriver(backend, "", "tag:")
			if err != nil {
				t.Fatal(err)
			}
		}
		manager := NewCache(nil, driver)
		if err := manager.Set("existing", "keep"); err != nil {
			t.Fatal(err)
		}
		if err := manager.Flush(); !errors.Is(err, redisDriver.ErrUnsafeRedisFlush) {
			t.Fatalf("没有拒绝未授权的全库清理: %v", err)
		}
		if value, found, err := manager.Get("existing"); err != nil || !found || value != "keep" {
			t.Fatalf("已拒绝的清理仍使业务值失效: namespaced=%t value=%v found=%t err=%v", namespaced, value, found, err)
		}
	}
}
