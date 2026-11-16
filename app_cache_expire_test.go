package framework

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
)

func TestAppCacheRejectsOverflowingExpireBeforeCreatingFileStore(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("此秒数在 32 位平台已由整数解析拒绝")
	}
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	const directory = "overflow-cache"
	// 这个秒数在 int64 中合法，乘以 time.Second 后会回绕到微小的正有效期。
	expire := json.Number("18446744074")
	manager, err := createAppCache(app, map[string]interface{}{
		"default": "disk",
		"stores": map[string]interface{}{
			"disk": map[string]interface{}{"type": "file", "path": directory, "expire": expire},
		},
	})
	if manager != nil {
		_ = manager.Close()
	}
	if manager != nil || !errors.Is(err, cache.ErrInvalidCacheTTL) || !strings.Contains(err.Error(), "disk") ||
		!strings.Contains(err.Error(), "expire") {
		t.Errorf("溢出的有效期应在启动配置中返回明确错误: %v", err)
	}
	path := filepath.Join(app.ApplicationRuntimePath(), directory)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("非法有效期不应创建缓存目录: %v", err)
	}
}

func TestAppCacheAcceptsExactSupportedExpireBoundary(t *testing.T) {
	const limit = int64((100 * 365 * 24 * time.Hour) / time.Second)
	for _, seconds := range []int64{0, 3600, limit} {
		if strconv.IntSize < 64 && seconds > math.MaxInt32 {
			continue
		}
		spec, err := parseAppCacheStore(nil, "memory", map[string]interface{}{"type": "memory", "expire": seconds})
		if err != nil || spec.expire != time.Duration(seconds)*time.Second {
			t.Errorf("支持的有效期 %d 秒应保持原值: %v, %v", seconds, spec.expire, err)
		}
	}
	if strconv.IntSize >= 64 {
		if _, err := parseAppCacheStore(nil, "memory", map[string]interface{}{"type": "memory", "expire": limit + 1}); !errors.Is(err, cache.ErrInvalidCacheTTL) {
			t.Fatalf("超过有效期上限一秒也应拒绝: %v", err)
		}
	}
}
