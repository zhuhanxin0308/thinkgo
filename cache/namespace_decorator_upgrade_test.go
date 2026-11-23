package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// 应用可以复用命名空间的键映射，同时自行提供嵌入驱动缺少的原子更新和租约策略。
type applicationNamespace struct {
	*cache.NamespaceDriver
	atomic  *cache.NamespaceDriver
	lockErr error
}

func (driver *applicationNamespace) UpdateContext(ctx context.Context, key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	return driver.atomic.UpdateContext(ctx, key, ttl, update)
}

func (driver *applicationNamespace) AcquireLockContext(context.Context, string, string, time.Duration) (bool, error) {
	return false, driver.lockErr
}

func TestNestedNamespaceRespectsApplicationDecorator(t *testing.T) {
	file, err := cacheDriver.NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wrap := func(backend cache.Driver, prefix string) *cache.NamespaceDriver {
		driver, err := cache.NewNamespaceDriver(backend, prefix, "")
		if err != nil {
			t.Fatal(err)
		}
		return driver
	}
	inner := wrap(file, "inner:")
	baseline := wrap(inner, "outer:")
	legacy := cache.NewCache(nil, baseline)
	if err := legacy.Set("account", "old"); err != nil {
		t.Fatal(err)
	}
	envelope, found, err := baseline.Get("account")
	if err != nil || !found {
		t.Fatalf("没有取得旧写入：%v %t %v", envelope, found, err)
	}
	if err := legacy.Forget("account"); err != nil {
		t.Fatal(err)
	}
	if err := baseline.Set("account", envelope, 0); err != nil {
		t.Fatal(err)
	}
	decorator := &applicationNamespace{
		NamespaceDriver: wrap(struct{ cache.Driver }{file}, "inner:"),
		atomic:          inner,
		lockErr:         errors.New("应用租约暂时不可用"),
	}
	upgraded := cache.NewCache(nil, wrap(decorator, "outer:"))
	if value, found, err := upgraded.Get("account"); err != nil || found {
		t.Errorf("装饰器下的失效记录重新可见：%v %t %v", value, found, err)
	}
	if err := upgraded.Set("account", "new"); err != nil {
		t.Fatal(err)
	}
	if value, found, err := legacy.Get("account"); err != nil || !found || value != "new" {
		t.Errorf("旧客户端无法读到经装饰器原子写入的新值：%v %t %v", value, found, err)
	}
	tagged, err := upgraded.Tag("accounts")
	if err != nil {
		t.Fatal(err)
	}
	if err := tagged.Set("sensitive", "value"); !errors.Is(err, decorator.lockErr) {
		t.Errorf("标签写入绕过了应用的租约检查：%v", err)
	}
	if _, found, err := baseline.Get("sensitive"); err != nil || found {
		t.Errorf("租约失败后不应留下业务值：%t %v", found, err)
	}
}
