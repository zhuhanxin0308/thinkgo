package framework

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type applicationManagerTestController struct{}

type applicationManagerTestProvider struct {
	name        string
	events      *[]string
	registerErr error
	bootErr     error
	shutdownErr error
}

func (provider *applicationManagerTestProvider) Register(*App) error {
	*provider.events = append(*provider.events, provider.name+":register")
	return provider.registerErr
}

func (provider *applicationManagerTestProvider) Boot(*App) error {
	*provider.events = append(*provider.events, provider.name+":boot")
	return provider.bootErr
}

func (provider *applicationManagerTestProvider) Shutdown(*App) error {
	*provider.events = append(*provider.events, provider.name+":shutdown")
	return provider.shutdownErr
}

type applicationManagerTestKernel struct {
	runs int
	err  error
}

func (kernel *applicationManagerTestKernel) Run() error {
	kernel.runs++
	return kernel.err
}

type blockingApplicationManagerTestKernel struct {
	started chan struct{}
	release chan struct{}
}

type blockingShutdownApplicationManagerTestProvider struct {
	started chan struct{}
	release chan struct{}
	err     error
}

func (provider *blockingShutdownApplicationManagerTestProvider) Register(*App) error {
	return nil
}

func (provider *blockingShutdownApplicationManagerTestProvider) Boot(*App) error {
	return nil
}

func (provider *blockingShutdownApplicationManagerTestProvider) Shutdown(*App) error {
	close(provider.started)
	<-provider.release
	return provider.err
}

func (kernel *blockingApplicationManagerTestKernel) Run() error {
	close(kernel.started)
	<-kernel.release
	return nil
}

func newApplicationManagerTestDefinition(name string, provider *applicationManagerTestProvider, callbackCount *int) ApplicationDefinition {
	return ApplicationDefinition{
		Name: name,
		Path: filepath.ToSlash(filepath.Join("app", name)),
		Register: func(app *App) error {
			(*callbackCount)++
			if err := app.RegisterController("Shared", &applicationManagerTestController{}); err != nil {
				return err
			}
			return app.RegisterProvider(provider)
		},
	}
}

func TestApplicationManagerBuildsIndependentAppsAndRunsProviders(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	events := make([]string, 0, 8)
	adminCallbackCount := 0
	indexCallbackCount := 0
	adminProvider := &applicationManagerTestProvider{name: "admin", events: &events}
	indexProvider := &applicationManagerTestProvider{name: "index", events: &events}
	definitions := []ApplicationDefinition{
		newApplicationManagerTestDefinition("index", indexProvider, &indexCallbackCount),
		newApplicationManagerTestDefinition("admin", adminProvider, &adminCallbackCount),
	}

	manager, err := newApplicationManager(basePath, definitions, true)
	if err != nil {
		t.Fatalf("创建应用管理器失败: %v", err)
	}
	if adminCallbackCount != 1 || indexCallbackCount != 1 {
		t.Fatalf("应用注册回调应各执行一次: admin=%d index=%d", adminCallbackCount, indexCallbackCount)
	}
	if _, ok := manager.Application("admin"); !ok {
		t.Fatal("管理器中缺少 admin 应用")
	}
	if manager.DefaultApplication() == nil || manager.DefaultApplication().ApplicationName != "index" {
		t.Fatal("默认应用应优先选择 index")
	}
	if got := manager.Applications(); len(got) != 2 {
		t.Fatalf("管理器应用快照数量错误: %d", len(got))
	}
	if manager.Applications()["admin"].ApplicationPath != filepath.Join(basePath, "app", "admin") {
		t.Fatal("admin 应用路径未归属应用目录")
	}
	if manager.Applications()["admin"].RuntimePath == manager.Applications()["index"].RuntimePath {
		t.Fatal("不同应用运行时目录不能相同")
	}

	if err := manager.Boot(); err != nil {
		t.Fatalf("应用管理器启动 provider 失败: %v", err)
	}
	if want := []string{"admin:register", "index:register", "admin:boot", "index:boot"}; !sameStringSlice(events, want) {
		t.Fatalf("provider 启动顺序错误: got %#v, want %#v", events, want)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("应用管理器关闭失败: %v", err)
	}
	if want := []string{"admin:register", "index:register", "admin:boot", "index:boot", "index:shutdown", "admin:shutdown"}; !sameStringSlice(events, want) {
		t.Fatalf("应用逆序关闭错误: got %#v, want %#v", events, want)
	}
}

// TestApplicationManagerRejectsConflictingDefaultApplicationSettings 验证默认应用配置冲突会在启动前失败。
func TestApplicationManagerRejectsConflictingDefaultApplicationSettings(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	for name, content := range map[string]string{
		"admin": `{"default_app":"admin"}`,
		"index": `{"default_app":"index"}`,
	} {
		configPath := filepath.Join(basePath, "app", name, "config")
		if err := os.MkdirAll(configPath, 0o755); err != nil {
			t.Fatalf("创建应用配置目录失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(configPath, "app.json"), []byte(content), 0o644); err != nil {
			t.Fatalf("写入应用 %s 默认配置失败: %v", name, err)
		}
	}
	definitions := []ApplicationDefinition{
		{Name: "admin", Path: "app/admin", Register: func(*App) error { return nil }},
		{Name: "index", Path: "app/index", Register: func(*App) error { return nil }},
	}
	manager, err := newApplicationManager(basePath, definitions, true)
	if manager != nil {
		t.Fatal("默认应用配置冲突时不应返回管理器")
	}
	if !errors.Is(err, ErrConflictingDefaultApplication) {
		t.Fatalf("默认应用冲突应返回 ErrConflictingDefaultApplication，实际为 %v", err)
	}
}

func TestApplicationManagerBootFailurePreventsKernelAndRollsBackApps(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	events := make([]string, 0, 8)
	adminProvider := &applicationManagerTestProvider{
		name:    "admin",
		events:  &events,
		bootErr: errors.New("admin provider boot failed"),
	}
	indexProvider := &applicationManagerTestProvider{name: "index", events: &events}
	definitions := []ApplicationDefinition{
		newApplicationManagerTestDefinition("admin", adminProvider, new(int)),
		newApplicationManagerTestDefinition("index", indexProvider, new(int)),
	}
	manager, err := newApplicationManager(basePath, definitions, true)
	if err != nil {
		t.Fatalf("创建应用管理器失败: %v", err)
	}

	if err := manager.Run(&applicationManagerTestKernel{}); !errors.Is(err, adminProvider.bootErr) {
		t.Fatalf("provider 启动错误应返回原始错误: %v", err)
	}
	if indexProvider.bootErr != nil {
		t.Fatal("测试 provider 配置错误")
	}
	if countString(events, "index:boot") != 0 {
		t.Fatal("前一个应用 provider 启动失败后不应继续启动后续应用")
	}
	if countString(events, "admin:shutdown") != 1 || countString(events, "index:shutdown") != 1 {
		t.Fatalf("启动失败后应逆序关闭所有已创建应用: %#v", events)
	}
}

func TestApplicationManagerStartupErrorPreventsProviderBoot(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	events := make([]string, 0, 4)
	provider := &applicationManagerTestProvider{name: "admin", events: &events}
	initializationErr := errors.New("admin initialization failed")
	definitions := []ApplicationDefinition{{
		Name: "admin",
		Path: "app/admin",
		Register: func(app *App) error {
			if err := app.RegisterProvider(provider); err != nil {
				return err
			}
			app.recordStartupError(initializationErr)
			return nil
		},
	}}
	manager, err := newApplicationManager(basePath, definitions, true)
	if err != nil {
		t.Fatalf("创建应用管理器失败: %v", err)
	}

	kernel := &applicationManagerTestKernel{}
	if err := manager.Run(kernel); err == nil || !errors.Is(err, initializationErr) {
		t.Fatalf("应用初始化错误应阻止宿主启动: %v", err)
	}
	if countString(events, "admin:boot") != 0 || kernel.runs != 0 {
		t.Fatalf("初始化错误时不应启动 provider 或宿主内核: err=%v events=%#v runs=%d", err, events, kernel.runs)
	}
}

// TestBuildApplicationManagerRejectsInitializationFailure 验证严格构建入口不会返回半初始化管理器。
func TestBuildApplicationManagerRejectsInitializationFailure(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	initializationErr := errors.New("strict manager initialization failed")
	events := make([]string, 0, 2)
	provider := &applicationManagerTestProvider{name: "admin", events: &events}
	definitions := []ApplicationDefinition{{
		Name: "admin",
		Path: "app/admin",
		Register: func(app *App) error {
			if err := app.RegisterProvider(provider); err != nil {
				return err
			}
			app.recordStartupError(initializationErr)
			return nil
		},
	}}

	manager, err := BuildApplicationManagerFromDefinitions(basePath, definitions, true)
	if manager != nil {
		t.Fatal("严格构建失败时不应返回半初始化管理器")
	}
	if !errors.Is(err, initializationErr) {
		t.Fatalf("严格构建应返回应用初始化错误，实际为 %v", err)
	}
	if countString(events, "admin:shutdown") != 1 {
		t.Fatalf("严格构建失败时应关闭已注册 Provider，实际事件为 %#v", events)
	}
}

// TestBuildApplicationManagerReturnsInitializedManager 验证严格构建成功后所有应用均已完成初始化。
func TestBuildApplicationManagerReturnsInitializedManager(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	definitions := []ApplicationDefinition{{
		Name:     "index",
		Path:     "app/index",
		Register: func(app *App) error { return nil },
	}}

	manager, err := BuildApplicationManagerFromDefinitions(basePath, definitions, true)
	if err != nil {
		t.Fatalf("严格构建管理器失败: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	app, ok := manager.Application("index")
	if !ok || app == nil || !app.Initialized() {
		t.Fatalf("严格构建成功后应用应已初始化: app=%v ok=%t", app, ok)
	}
}

func TestApplicationManagerRunUsesOneKernelAndIsIdempotentOnClose(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	events := make([]string, 0, 4)
	provider := &applicationManagerTestProvider{name: "index", events: &events}
	definitions := []ApplicationDefinition{newApplicationManagerTestDefinition("index", provider, new(int))}
	manager, err := newApplicationManager(basePath, definitions, true)
	if err != nil {
		t.Fatalf("创建应用管理器失败: %v", err)
	}
	kernel := &applicationManagerTestKernel{}
	if err := manager.Run(kernel); err != nil {
		t.Fatalf("应用管理器运行失败: %v", err)
	}
	if kernel.runs != 1 {
		t.Fatalf("宿主内核应只运行一次: %d", kernel.runs)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("运行结束后重复关闭应幂等: %v", err)
	}
}

func TestApplicationManagerCloseRejectsBooting(t *testing.T) {
	manager := &ApplicationManager{booting: true}
	if err := manager.Close(); !errors.Is(err, ErrApplicationManagerRunning) {
		t.Fatalf("管理器启动中必须拒绝关闭，实际错误为 %v", err)
	}
}

func TestApplicationManagerCloseRejectsRunPending(t *testing.T) {
	manager := &ApplicationManager{runPending: true}
	if err := manager.Close(); !errors.Is(err, ErrApplicationManagerRunning) {
		t.Fatalf("管理器等待 Run 启动时必须拒绝关闭，实际错误为 %v", err)
	}
}

func TestApplicationManagerBootRejectsRunPending(t *testing.T) {
	manager := &ApplicationManager{runPending: true}
	if err := manager.Boot(); !errors.Is(err, ErrApplicationManagerRunning) {
		t.Fatalf("Run 已占有启动权时普通 Boot 必须被拒绝，实际错误为 %v", err)
	}
}

// TestApplicationManagerOwnsDirectApplicationLifecycle 验证托管应用不能绕过管理器直接运行或关闭。
func TestApplicationManagerOwnsDirectApplicationLifecycle(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	manager, err := newApplicationManager(basePath, []ApplicationDefinition{{
		Name: "index",
		Path: "app/index",
		Register: func(*App) error {
			return nil
		},
	}}, true)
	if err != nil {
		t.Fatalf("创建应用管理器失败: %v", err)
	}
	app, exists := manager.Application("index")
	if !exists || app == nil {
		t.Fatal("管理器应返回托管应用")
	}
	if err := app.Run(); !errors.Is(err, ErrApplicationManagerOwnsLifecycle) {
		t.Fatalf("托管应用直接 Run 应返回生命周期所有权错误，实际为 %v", err)
	}
	if err := app.Close(); !errors.Is(err, ErrApplicationManagerOwnsLifecycle) {
		t.Fatalf("托管应用直接 Close 应返回生命周期所有权错误，实际为 %v", err)
	}
	if err := manager.Boot(); err != nil {
		t.Fatalf("管理器 Boot 失败: %v", err)
	}
	if err := app.Close(); !errors.Is(err, ErrApplicationManagerOwnsLifecycle) {
		t.Fatalf("Boot 后托管应用仍不能直接 Close，实际为 %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("管理器 Close 失败: %v", err)
	}
	if state := app.State(); state != ApplicationStateClosed {
		t.Fatalf("管理器关闭后应用状态应为 Closed，实际为 %s", state)
	}
}

func TestApplicationManagerRunRejectsConcurrentCallsAndClose(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	provider := &applicationManagerTestProvider{name: "index", events: new([]string)}
	manager, err := newApplicationManager(basePath, []ApplicationDefinition{
		newApplicationManagerTestDefinition("index", provider, new(int)),
	}, true)
	if err != nil {
		t.Fatalf("创建应用管理器失败: %v", err)
	}
	kernel := &blockingApplicationManagerTestKernel{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	runResult := make(chan error, 1)
	var releaseOnce sync.Once
	releaseKernel := func() {
		releaseOnce.Do(func() { close(kernel.release) })
	}
	t.Cleanup(releaseKernel)
	go func() {
		runResult <- manager.Run(kernel)
	}()
	<-kernel.started
	app, exists := manager.Application("index")
	if !exists || app == nil {
		t.Fatal("运行中的管理器应返回托管应用")
	}
	if state := app.State(); state != ApplicationStateRunning {
		t.Fatalf("管理器运行期间应用状态应为 Running，实际为 %s", state)
	}
	if err = app.Run(); !errors.Is(err, ErrApplicationManagerRunning) {
		t.Fatalf("管理器运行期间应用直接 Run 应返回运行中错误，实际为 %v", err)
	}
	if err = app.Close(); !errors.Is(err, ErrApplicationManagerRunning) {
		t.Fatalf("管理器运行期间应用直接 Close 应返回运行中错误，实际为 %v", err)
	}

	if err = manager.Run(&applicationManagerTestKernel{}); !errors.Is(err, ErrApplicationManagerRunning) {
		t.Fatalf("并发 Run 必须返回运行中错误，实际为 %v", err)
	}
	if err = manager.Close(); !errors.Is(err, ErrApplicationManagerRunning) {
		t.Fatalf("运行中 Close 必须返回运行中错误，实际为 %v", err)
	}
	releaseKernel()
	if err = <-runResult; err != nil {
		t.Fatalf("首个 Run 退出失败: %v", err)
	}
	if state := app.State(); state != ApplicationStateClosed {
		t.Fatalf("管理器 Run 退出后应用状态应为 Closed，实际为 %s", state)
	}
}

func TestApplicationManagerConcurrentCloseWaitsForSameError(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	shutdownErr := errors.New("shutdown failed")
	provider := &blockingShutdownApplicationManagerTestProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
		err:     shutdownErr,
	}
	manager, err := newApplicationManager(basePath, []ApplicationDefinition{{
		Name: "index",
		Path: "app/index",
		Register: func(app *App) error {
			return app.RegisterProvider(provider)
		},
	}}, true)
	if err != nil {
		t.Fatalf("创建应用管理器失败: %v", err)
	}
	if err = manager.Boot(); err != nil {
		t.Fatalf("启动应用管理器失败: %v", err)
	}
	var releaseOnce sync.Once
	releaseShutdown := func() {
		releaseOnce.Do(func() { close(provider.release) })
	}
	t.Cleanup(releaseShutdown)

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- manager.Close()
	}()
	<-provider.started
	secondStarted := make(chan struct{})
	secondResult := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondResult <- manager.Close()
	}()
	<-secondStarted
	const closeWaitSchedulerYields = 100
	for index := 0; index < closeWaitSchedulerYields; index++ {
		runtime.Gosched()
	}
	select {
	case earlyErr := <-secondResult:
		t.Fatalf("并发 Close 不得在实际关闭完成前返回，实际为 %v", earlyErr)
	default:
	}

	releaseShutdown()
	firstErr := <-firstResult
	secondErr := <-secondResult
	if !errors.Is(firstErr, shutdownErr) || !errors.Is(secondErr, shutdownErr) {
		t.Fatalf("并发 Close 必须返回同一个关闭错误，first=%v second=%v", firstErr, secondErr)
	}
}

func TestApplicationManagerRunRejectsNewKernelWhileClosing(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	provider := &blockingShutdownApplicationManagerTestProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager, err := newApplicationManager(basePath, []ApplicationDefinition{{
		Name: "index",
		Path: "app/index",
		Register: func(app *App) error {
			return app.RegisterProvider(provider)
		},
	}}, true)
	if err != nil {
		t.Fatalf("创建应用管理器失败: %v", err)
	}
	var releaseOnce sync.Once
	releaseShutdown := func() {
		releaseOnce.Do(func() { close(provider.release) })
	}
	t.Cleanup(releaseShutdown)
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- manager.Run(&applicationManagerTestKernel{})
	}()
	<-provider.started

	secondKernel := &applicationManagerTestKernel{}
	secondErr := manager.Run(secondKernel)
	if !errors.Is(secondErr, ErrApplicationManagerClosed) && !errors.Is(secondErr, ErrApplicationManagerRunning) {
		t.Fatalf("关闭期间重复 Run 必须返回关闭或运行状态错误，实际为 %v", secondErr)
	}
	if secondKernel.runs != 0 {
		t.Fatalf("关闭期间第二个 Kernel 不得执行，实际执行 %d 次", secondKernel.runs)
	}
	releaseShutdown()
	if err = <-firstResult; err != nil {
		t.Fatalf("首个 Run 退出失败: %v", err)
	}
}

func TestApplicationManagerRunJoinsKernelAndReverseCloseErrors(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	events := make([]string, 0, 6)
	adminCloseErr := errors.New("admin shutdown failed")
	indexCloseErr := errors.New("index shutdown failed")
	kernelErr := errors.New("kernel failed")
	definitions := []ApplicationDefinition{
		newApplicationManagerTestDefinition("index", &applicationManagerTestProvider{name: "index", events: &events, shutdownErr: indexCloseErr}, new(int)),
		newApplicationManagerTestDefinition("admin", &applicationManagerTestProvider{name: "admin", events: &events, shutdownErr: adminCloseErr}, new(int)),
	}
	manager, err := newApplicationManager(basePath, definitions, true)
	if err != nil {
		t.Fatalf("创建应用管理器失败: %v", err)
	}

	runErr := manager.Run(&applicationManagerTestKernel{err: kernelErr})
	for _, target := range []error{kernelErr, adminCloseErr, indexCloseErr} {
		if !errors.Is(runErr, target) {
			t.Fatalf("Run 应保留内核和全部关闭错误 %v，实际为 %v", target, runErr)
		}
	}
	want := []string{"admin:register", "index:register", "admin:boot", "index:boot", "index:shutdown", "admin:shutdown"}
	if !sameStringSlice(events, want) {
		t.Fatalf("启动和逆序关闭顺序错误: got %#v, want %#v", events, want)
	}
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func countString(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}

func TestApplicationManagerErrorContainsApplicationName(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	providerErr := errors.New("provider register failed")
	provider := &applicationManagerTestProvider{name: "admin", events: new([]string), registerErr: providerErr}
	definitions := []ApplicationDefinition{newApplicationManagerTestDefinition("admin", provider, new(int))}
	manager, err := newApplicationManager(basePath, definitions, true)
	if err != nil {
		t.Fatalf("创建应用管理器失败: %v", err)
	}
	if runErr := manager.Boot(); runErr == nil || !errors.Is(runErr, providerErr) || !containsText(runErr.Error(), "admin") {
		t.Fatalf("应用错误应包含应用名和原始错误: %v", runErr)
	}
}

func containsText(value, target string) bool {
	return strings.Contains(value, target)
}
