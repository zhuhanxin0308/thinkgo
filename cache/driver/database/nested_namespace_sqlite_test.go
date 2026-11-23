//go:build cgo

package database

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
)

// 两个子空间共用真实数据库，公开管理器对最长业务键的写、读和删除应各自独立。
func TestNestedNamespaceDBPublicLongKey(t *testing.T) {
	_, store, _ := newGenerationInterleavingDrivers(t, "")
	outer, err := cache.NewNamespaceDriver(store, "shared:", "")
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("k", 1024)
	managers := make(map[string]*cache.Cache)
	for _, tenant := range []string{"甲", "乙"} {
		nested, err := cache.NewNamespaceDriver(outer, tenant, "")
		if err != nil {
			t.Fatal(err)
		}
		manager := cache.NewCache(nil, nested)
		managers[tenant] = manager
		if err := manager.Set(key, tenant, 0); err != nil {
			t.Fatalf("租户 %s 不能通过公开接口写入合法长键：%v", tenant, err)
		}
	}
	for tenant, manager := range managers {
		if got, found, err := manager.Get(key); err != nil || !found || got != tenant {
			t.Fatalf("租户 %s 未读到自己的值：%v %t %v", tenant, got, found, err)
		}
	}
	if err := managers["甲"].Forget(key); err != nil {
		t.Fatal(err)
	}
	if _, found, err := managers["甲"].Get(key); err != nil || found {
		t.Fatalf("已删除租户仍命中：%t %v", found, err)
	}
	if got, found, err := managers["乙"].Get(key); err != nil || !found || got != "乙" {
		t.Fatalf("删除波及相邻租户：%v %t %v", got, found, err)
	}
}

func TestNestedNamespaceDBGuardedWriteKeepsIntegerPrecision(t *testing.T) {
	_, store, _ := newGenerationInterleavingDrivers(t, "")
	outer, err := cache.NewNamespaceDriver(store, "shared:", "")
	if err != nil {
		t.Fatal(err)
	}
	tenanted, err := cache.NewNamespaceDriver(outer, "tenant:", "")
	if err != nil {
		t.Fatal(err)
	}
	manager := cache.NewCache(nil, tenanted)
	const exact = int64(math.MaxInt64)
	if _, err := manager.RememberWithLock("sequence", 0, time.Minute, func() (interface{}, error) { return exact, nil }); err != nil {
		t.Fatal(err)
	}
	if got, found, err := manager.Get("sequence"); err != nil || !found || got != exact {
		t.Fatalf("数据库租约写回的整数无法以原类型和精度读取：%v %t %v", got, found, err)
	}
}
