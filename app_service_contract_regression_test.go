package framework

import (
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/metrics"
)

// TestApplicationClosesEveryReplacedResource 验证多次替换也释放中间实例，不依赖 Provider 留下最初指针。
func TestApplicationClosesEveryReplacedResource(t *testing.T) {
	app := &App{container: NewContainer()}
	registerAppShutdownProviders(t, app, &appCacheProvider{})
	var drivers []*appRunCacheDriver
	for index := 0; index < 3; index++ {
		driver := &appRunCacheDriver{Memory: cacheDriver.NewMemory()}
		drivers = append(drivers, driver)
		if err := app.Instance(serviceKeyCache, cache.NewCache(nil, driver)); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	for index, driver := range drivers {
		if driver.closes != 1 {
			t.Fatalf("第 %d 个资源关闭次数为 %d", index, driver.closes)
		}
	}
}

// TestApplicationResolutionEntrancesAgree 验证必需兼容入口与类型化入口共享状态错误语义。
func TestApplicationResolutionEntrancesAgree(t *testing.T) {
	app := &App{container: NewContainer()}
	key, err := NewTypedServiceKey[*int]("missing")
	if err != nil {
		t.Fatal(err)
	}
	_, makeErr := app.Make("missing")
	_, typedErr := ResolveTyped(app, key)
	_, serviceErr := app.ResolveService("missing")
	for _, result := range []error{makeErr, typedErr, serviceErr} {
		if !errors.Is(result, ErrServiceNotReady) {
			t.Fatalf("应用解析入口错误语义不一致: %v", result)
		}
	}
}

// TestShutdownDoesNotResolveUnusedLazyDatabase 验证关闭期间不会执行从未使用的数据库工厂。
func TestShutdownDoesNotResolveUnusedLazyDatabase(t *testing.T) {
	container := NewContainer()
	calls := 0
	container.Bind(serviceKeyDB, func() (*db.DB, error) {
		calls++
		return nil, db.ErrDatabaseUnavailable
	})
	app := &App{container: container}
	if err := (&appDatabaseProvider{}).Shutdown(app); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("关闭操作触发了 %d 次惰性数据库连接", calls)
	}
	created := &db.DB{}
	container.Instance(serviceKeyDB, created)
	if got, exists := container.LookupInstance(serviceKeyDB); !exists || got != created {
		t.Fatal("关闭阶段遗漏了已创建的实例")
	}
}

// TestRuntimeMetricsFollowsFinalConfiguration 验证最终关闭配置也关闭采集器，不能保留先前开启状态。
func TestRuntimeMetricsFollowsFinalConfiguration(t *testing.T) {
	app := &App{config: config.NewConfig(), metrics: metrics.NewRegistry()}
	for _, enabled := range []bool{true, false, true} {
		if err := app.config.Set("app.metrics_enable", enabled); err != nil {
			t.Fatal(err)
		}
		app.applyApplicationRuntimeConfig()
		if app.metrics.Enabled() != enabled || app.metricsEnabled != enabled {
			t.Fatalf("最终开关与实际采集状态不一致: 期望 %t，实际 %t", enabled, app.metrics.Enabled())
		}
	}
}
