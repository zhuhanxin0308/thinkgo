package framework

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/cache"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/log"
)

type appRunLogDriver struct {
	closed   bool
	closes   int
	closeErr error
	entries  []*log.LogEntry
}

func (d *appRunLogDriver) SaveEntries(entries []*log.LogEntry) error {
	d.entries = append(d.entries, entries...)
	return nil
}

func (d *appRunLogDriver) WriteEntry(entry *log.LogEntry) error {
	d.entries = append(d.entries, entry)
	return nil
}

func (d *appRunLogDriver) Close() error {
	d.closed = true
	d.closes++
	return d.closeErr
}

type appRunConnection struct {
	identity db.ConnectionID
	closed   bool
	closes   int
	closeErr error
}

func (c *appRunConnection) ConnectionID() db.ConnectionID {
	if c.identity == "" {
		c.identity = db.NewConnectionID("app-run-test")
	}
	return c.identity
}

func (c *appRunConnection) Select(context.Context, db.SelectRequest) ([]map[string]interface{}, error) {
	return nil, nil
}

func (c *appRunConnection) Insert(context.Context, db.InsertRequest) (db.InsertResult, error) {
	return db.InsertResult{}, nil
}

func (c *appRunConnection) Update(context.Context, db.UpdateRequest) (db.UpdateResult, error) {
	return db.UpdateResult{}, nil
}

func (c *appRunConnection) Delete(context.Context, db.DeleteRequest) (db.DeleteResult, error) {
	return db.DeleteResult{}, nil
}

func (c *appRunConnection) Count(context.Context, db.CountRequest) (int64, error) {
	return 0, nil
}

func (c *appRunConnection) Close() error {
	c.closed = true
	c.closes++
	return c.closeErr
}

type appRunKernel struct {
	err error
}

func (k *appRunKernel) Run() error {
	return k.err
}

type blockingAppRunKernel struct {
	started chan struct{}
	release chan struct{}
}

func (k *blockingAppRunKernel) Run() error {
	close(k.started)
	<-k.release
	return nil
}

type panickingAppRunKernel struct{}

func (*panickingAppRunKernel) Run() error { panic("kernel panic") }

type panickingAppRunConnection struct {
	appRunConnection
}

func (c *panickingAppRunConnection) Close() error { panic("database close panic") }

type appRunCacheDriver struct {
	*cacheDriver.Memory
	closes   int
	closeErr error
}

func (d *appRunCacheDriver) Close() error {
	d.closes++
	return d.closeErr
}

// registerAppShutdownProviders 为手工构造的测试应用显式安装资源释放 Provider。
func registerAppShutdownProviders(t *testing.T, app *App, providers ...ServiceProvider) {
	t.Helper()
	if app.container == nil {
		app.container = NewContainer()
	}
	for _, resource := range []struct {
		name     string
		instance any
	}{
		{serviceKeyLog, app.log}, {serviceKeyCache, app.cache}, {serviceKeyDBManager, app.dbManager},
		{serviceKeyDB, app.db}, {serviceKeyFilesystem, app.filesystem}, {serviceKeySession, app.session},
	} {
		if !isNilServiceInstance(resource.instance) {
			if err := app.Instance(resource.name, resource.instance); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, provider := range providers {
		if err := app.RegisterProvider(provider); err != nil {
			t.Fatalf("注册资源释放 Provider 失败: %v", err)
		}
	}
}

// TestAppRunShutsDownLogAndDatabase 验证应用退出时会关闭日志与数据库资源。
func TestAppRunShutsDownLogAndDatabase(t *testing.T) {
	driver := &appRunLogDriver{}
	conn := &appRunConnection{}
	app := &App{
		log:    log.NewLog(driver),
		db:     db.NewDB(conn),
		Kernel: &appRunKernel{},
	}
	registerAppShutdownProviders(t, app, &appLogProvider{}, &appDatabaseProvider{})

	if err := app.Run(); err != nil {
		t.Fatalf("应用运行失败: %v", err)
	}

	if !driver.closed {
		t.Fatal("App.Run 结束后应关闭日志驱动")
	}
	if !conn.closed {
		t.Fatal("App.Run 结束后应关闭数据库连接")
	}
}

// TestAppRunReturnsStartupError 验证启动错误通过返回值暴露且仍会释放资源。
func TestAppRunReturnsStartupError(t *testing.T) {
	driver := &appRunLogDriver{}
	startupErr := errors.New("startup failed")
	app := &App{
		log:        log.NewLog(driver),
		startupErr: startupErr,
	}
	registerAppShutdownProviders(t, app, &appLogProvider{})

	if err := app.Run(); !errors.Is(err, startupErr) {
		t.Fatalf("Run 应返回启动错误，实际为 %v", err)
	}
	if !driver.closed {
		t.Fatal("启动失败时也应关闭日志驱动")
	}
}

// TestAppCloseAggregatesErrorsAndIsIdempotent 验证资源关闭错误完整返回且每项资源只关闭一次。
func TestAppCloseAggregatesErrorsAndIsIdempotent(t *testing.T) {
	logErr := errors.New("log close failed")
	dbErr := errors.New("database close failed")
	driver := &appRunLogDriver{closeErr: logErr}
	connection := &appRunConnection{closeErr: dbErr}
	app := &App{log: log.NewLog(driver), db: db.NewDB(connection)}
	registerAppShutdownProviders(t, app, &appLogProvider{}, &appDatabaseProvider{})

	first := app.Close()
	second := app.Close()
	if !errors.Is(first, logErr) || !errors.Is(first, dbErr) {
		t.Fatalf("Close 应聚合日志和数据库错误，实际为 %v", first)
	}
	if !errors.Is(second, logErr) || !errors.Is(second, dbErr) {
		t.Fatalf("重复 Close 应返回首次关闭结果，实际为 %v", second)
	}
	if driver.closes != 1 || connection.closes != 1 {
		t.Fatalf("资源应只关闭一次，log=%d db=%d", driver.closes, connection.closes)
	}

	panicDriver := &appRunLogDriver{}
	panicApp := &App{
		log: log.NewLog(panicDriver),
		db:  db.NewDB(&panickingAppRunConnection{}),
	}
	registerAppShutdownProviders(t, panicApp, &appLogProvider{}, &appDatabaseProvider{})
	if err := panicApp.Close(); !errors.Is(err, ErrResourceClosePanic) {
		t.Fatalf("数据库 Close panic 应转换为 ErrResourceClosePanic，实际为 %v", err)
	}
	if !panicDriver.closed {
		t.Fatal("数据库关闭 panic 后仍应继续关闭日志")
	}
}

// TestAppCloseIncludesCacheLifecycle 验证应用关闭会释放缓存连接，并稳定聚合缓存关闭错误。
func TestAppCloseIncludesCacheLifecycle(t *testing.T) {
	cacheErr := errors.New("cache close failed")
	driver := &appRunCacheDriver{Memory: cacheDriver.NewMemory(), closeErr: cacheErr}
	app := &App{cache: cache.NewCache(nil, driver)}
	registerAppShutdownProviders(t, app, &appCacheProvider{})
	first := app.Close()
	second := app.Close()
	if !errors.Is(first, cacheErr) || !errors.Is(second, cacheErr) {
		t.Fatalf("应用关闭应稳定返回缓存错误: first=%v second=%v", first, second)
	}
	if driver.closes != 1 {
		t.Fatalf("缓存驱动应只关闭一次，实际为 %d", driver.closes)
	}
}

// TestAppCloseClosesReplacedCache 验证容器替换资源后，原资源和当前资源都能被安全释放。
func TestAppCloseClosesReplacedCache(t *testing.T) {
	originalDriver := &appRunCacheDriver{Memory: cacheDriver.NewMemory()}
	replacementDriver := &appRunCacheDriver{Memory: cacheDriver.NewMemory()}
	original := cache.NewCache(nil, originalDriver)
	replacement := cache.NewCache(nil, replacementDriver)
	app := &App{container: NewContainer(), cache: original}
	provider := &appCacheProvider{}
	registerAppShutdownProviders(t, app, provider)
	app.Instance(serviceKeyCache, replacement)

	if app.cache != replacement {
		t.Fatal("显式替换缓存服务后，应用内部快照应同步到当前实例")
	}
	if err := app.Close(); err != nil {
		t.Fatalf("关闭替换缓存后的应用失败: %v", err)
	}
	if originalDriver.closes != 1 || replacementDriver.closes != 1 {
		t.Fatalf("原缓存和替换缓存都应各关闭一次，original=%d replacement=%d", originalDriver.closes, replacementDriver.closes)
	}
}

// TestAppRunReturnsKernelAndMissingKernelErrors 验证内核错误不会被日志或标准输出吞掉。
func TestAppRunReturnsKernelAndMissingKernelErrors(t *testing.T) {
	kernelErr := errors.New("kernel failed")
	app := &App{Kernel: &appRunKernel{err: kernelErr}}
	if err := app.Run(); !errors.Is(err, kernelErr) {
		t.Fatalf("Run 应返回内核错误，实际为 %v", err)
	}

	missingKernel := &App{}
	if err := missingKernel.Run(); !errors.Is(err, ErrKernelUnavailable) {
		t.Fatalf("缺失内核应返回 ErrKernelUnavailable，实际为 %v", err)
	}
	panicApp := &App{Kernel: &panickingAppRunKernel{}}
	if err := panicApp.Run(); !errors.Is(err, ErrKernelPanic) {
		t.Fatalf("内核 panic 应转换为 ErrKernelPanic，实际为 %v", err)
	}
}

// TestAppRunRejectsConcurrentAndPostCloseRestarts 验证单个应用实例不能并发运行或在关闭后重启。
func TestAppRunRejectsConcurrentAndPostCloseRestarts(t *testing.T) {
	kernel := &blockingAppRunKernel{started: make(chan struct{}), release: make(chan struct{})}
	app := &App{Kernel: kernel}
	firstDone := make(chan error, 1)
	go func() { firstDone <- app.Run() }()
	<-kernel.started
	if err := app.Run(); !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("并发 Run 应返回 ErrApplicationRunning，实际为 %v", err)
	}
	if err := app.Close(); !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("运行中直接 Close 应返回 ErrApplicationRunning，实际为 %v", err)
	}
	if err := app.Initialize(); !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("运行中 Initialize 应返回 ErrApplicationRunning，实际为 %v", err)
	}
	close(kernel.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("首次 Run 失败: %v", err)
	}
	if err := app.Run(); !errors.Is(err, ErrApplicationClosed) {
		t.Fatalf("关闭后 Run 应返回 ErrApplicationClosed，实际为 %v", err)
	}
	if err := app.Initialize(); !errors.Is(err, ErrApplicationClosed) {
		t.Fatalf("关闭后 Initialize 应返回 ErrApplicationClosed，实际为 %v", err)
	}
}

// TestAppServiceMutationIsFrozenAfterRunning 验证应用进入运行态后不会再替换容器服务，避免请求线程观察到半更新快照。
func TestAppServiceMutationIsFrozenAfterRunning(t *testing.T) {
	kernel := &blockingAppRunKernel{started: make(chan struct{}), release: make(chan struct{})}
	app := &App{container: NewContainer(), Kernel: kernel}
	app.Instance("runtime.value", "before")

	runDone := make(chan error, 1)
	go func() { runDone <- app.Run() }()
	<-kernel.started
	app.Instance("runtime.value", "after")
	if got, err := app.Make("runtime.value"); err != nil || got != "before" {
		t.Fatalf("运行态不应接受服务替换，value=%#v err=%v", got, err)
	}
	close(kernel.release)
	if err := <-runDone; err != nil {
		t.Fatalf("应用运行失败: %v", err)
	}
}

// TestApplicationLifecycleRejectsNilReceiverAndRecoversGCStopPanic 验证 nil 接收者与后台停止回调异常均可观测。
func TestApplicationLifecycleRejectsNilReceiverAndRecoversGCStopPanic(t *testing.T) {
	var nilApp *App
	if err := nilApp.Run(); !errors.Is(err, ErrNilApplication) {
		t.Fatalf("nil App.Run 应返回 ErrNilApplication，实际为 %v", err)
	}
	if err := nilApp.Close(); !errors.Is(err, ErrNilApplication) {
		t.Fatalf("nil App.Close 应返回 ErrNilApplication，实际为 %v", err)
	}
	if err := nilApp.Initialize(); !errors.Is(err, ErrNilApplication) {
		t.Fatalf("nil App.Initialize 应返回 ErrNilApplication，实际为 %v", err)
	}
	if err := nilApp.StartupError(); !errors.Is(err, ErrNilApplication) {
		t.Fatalf("nil App.StartupError 应返回 ErrNilApplication，实际为 %v", err)
	}

	called := false
	if err := safeStopSessionGarbageCollector(func() { called = true }); err != nil || !called {
		t.Fatalf("正常停止回调应执行一次，called=%v err=%v", called, err)
	}
	if err := safeStopSessionGarbageCollector(func() { panic("gc stop panic") }); err == nil {
		t.Fatal("停止回调 panic 应转换为错误")
	}
	app := &App{}
	registerAppShutdownProviders(t, app, &appSessionProvider{
		stopGarbageCollector: func() { panic("gc stop panic") },
	})
	if err := app.Close(); err == nil {
		t.Fatal("App.Close 应返回会话回收停止错误")
	}
}

// TestStartupErrorsAreBounded 验证重复初始化故障不会让应用错误链无限增长。
func TestStartupErrorsAreBounded(t *testing.T) {
	app := &App{}
	for index := 0; index < maxStartupErrors+20; index++ {
		app.recordStartupError(errors.New("startup failure"))
	}
	app.startupMu.RLock()
	stored := app.startupErrorCount
	omitted := app.startupErrorOmitted
	app.startupMu.RUnlock()
	if stored != maxStartupErrors || omitted != 20 {
		t.Fatalf("启动错误应限制为 %d 条，stored=%d omitted=%d", maxStartupErrors, stored, omitted)
	}
	if err := app.StartupError(); err == nil || !strings.Contains(err.Error(), "省略 20 条") {
		t.Fatalf("StartupError 应报告省略数量，实际为 %v", err)
	}
}
