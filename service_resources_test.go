package framework

import (
	"errors"
	"sync"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

type blockingOwnedCacheDriver struct {
	*cacheDriver.Memory
	started chan struct{}
	release chan struct{}
}

func (driver *blockingOwnedCacheDriver) Close() error {
	close(driver.started)
	<-driver.release
	return nil
}

// TestResourceRegistrationDuringClose 验证关闭尚未完成时也已经封闭服务的登记窗口。
func TestResourceRegistrationDuringClose(t *testing.T) {
	var resources serviceResources
	driver := &blockingOwnedCacheDriver{
		Memory: cacheDriver.NewMemory(), started: make(chan struct{}), release: make(chan struct{}),
	}
	instance := cache.NewCache(nil, driver)
	if err := resources.track(serviceKeyCache, instance); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- resources.close(serviceKeyCache) }()
	<-driver.started
	registrationErr := resources.track(serviceKeyCache, instance)
	close(driver.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(registrationErr, ErrApplicationClosed) {
		t.Fatalf("资源关闭期间仍允许重新登记: %v", registrationErr)
	}
}

// TestResourceOwnershipSurvivesBindingFailure 验证新创建资源的绑定失败后仍由应用回收。
func TestResourceOwnershipSurvivesBindingFailure(t *testing.T) {
	app := &App{}
	driver := &appRunCacheDriver{Memory: cacheDriver.NewMemory()}
	if err := app.installManagedService(serviceKeyCache, cache.NewCache(nil, driver)); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("缺少容器时应报告绑定失败: %v", err)
	}
	if err := app.closeServiceResources(); err != nil {
		t.Fatal(err)
	}
	if driver.closes != 1 {
		t.Fatalf("绑定失败的资源关闭次数为 %d", driver.closes)
	}
}

// TestResourceRetirementAndLateCreation 验证提前释放和关闭后的新资源都不会泄漏或重复关闭。
func TestResourceRetirementAndLateCreation(t *testing.T) {
	var resources serviceResources
	driver := &appRunCacheDriver{Memory: cacheDriver.NewMemory()}
	instance := cache.NewCache(nil, driver)
	if err := resources.track(serviceKeyCache, instance); err != nil {
		t.Fatal(err)
	}
	if err := resources.retire(instance); err != nil {
		t.Fatal(err)
	}
	if err := resources.track(serviceKeyCache, instance); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("已释放资源不能重新使用: %v", err)
	}
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			if err := resources.close(); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	if driver.closes != 1 {
		t.Fatalf("提前释放的资源重复关闭: %d", driver.closes)
	}
	lateDriver := &appRunCacheDriver{Memory: cacheDriver.NewMemory()}
	if err := resources.track(serviceKeyCache, cache.NewCache(nil, lateDriver)); !errors.Is(err, ErrApplicationClosed) {
		t.Fatalf("关闭后的新资源应立即回收并返回状态错误: %v", err)
	}
	if lateDriver.closes != 1 {
		t.Fatalf("关闭后的新资源泄漏: %d", lateDriver.closes)
	}
}
