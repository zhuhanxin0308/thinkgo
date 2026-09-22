package framework

import (
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/config"
)

// TestApplicationContainerDirectMutationsHonorLifecycle 验证公开容器指针不能绕过应用运行态和关闭态门禁。
func TestApplicationContainerDirectMutationsHonorLifecycle(t *testing.T) {
	kernel := &blockingAppRunKernel{started: make(chan struct{}), release: make(chan struct{})}
	app := &App{container: NewContainer(), Kernel: kernel, lifecycle: appLifecycle{state: ApplicationStateConstructed}}
	container := app.Container()
	if err := container.TryInstance("coherence.value", "before"); err != nil {
		t.Fatalf("运行前直接注册实例失败: %v", err)
	}

	runDone := make(chan error, 1)
	go func() { runDone <- app.Run() }()
	<-kernel.started

	mutations := []struct {
		name string
		call func() error
	}{
		{name: "bind", call: func() error { return container.TryBind("coherence.bind", "blocked") }},
		{name: "factory", call: func() error { return container.TryBindFactory("coherence.factory", func() string { return "blocked" }) }},
		{name: "scoped", call: func() error { return container.TryBindScoped("coherence.scoped", func() string { return "blocked" }) }},
		{name: "instance", call: func() error { return container.TryInstance("coherence.instance", "blocked") }},
		{name: "delete", call: func() error { return container.TryDelete("coherence.value") }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if err := mutation.call(); !errors.Is(err, ErrApplicationRunning) {
				t.Fatalf("运行态直接变更应返回 ErrApplicationRunning，实际为 %v", err)
			}
		})
	}
	if value, err := app.Make("coherence.value"); err != nil || value != "before" {
		t.Fatalf("被拒绝的删除不应修改原绑定: value=%#v err=%v", value, err)
	}
	assertContainerMutationPanic(t, ErrApplicationRunning, func() {
		container.Bind("coherence.panic", "blocked")
	})

	close(kernel.release)
	if err := <-runDone; err != nil {
		t.Fatalf("结束测试应用失败: %v", err)
	}
	if err := container.TryDelete("coherence.value"); !errors.Is(err, ErrApplicationClosed) {
		t.Fatalf("关闭态直接变更应返回 ErrApplicationClosed，实际为 %v", err)
	}
	assertContainerMutationPanic(t, ErrApplicationClosed, func() {
		container.Delete("coherence.value")
	})
}

// TestApplicationContainerBuiltInMutationIsCoherent 验证内置服务只能被类型安全地同步替换，不能形成双重真相。
func TestApplicationContainerBuiltInMutationIsCoherent(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	container := app.Container()
	original := app.Config()
	replacement := config.NewConfig()

	if err := container.TryInstance(serviceKeyConfig, replacement); err != nil {
		t.Fatalf("直接替换配置服务失败: %v", err)
	}
	resolved, err := app.Make(serviceKeyConfig)
	if err != nil || resolved != replacement || app.Config() != replacement {
		t.Fatalf("配置容器与 App 快照未同步: resolved=%p facade=%p err=%v", resolved, app.Config(), err)
	}

	if err = container.TryInstance(serviceKeyConfig, "wrong-type"); !errors.Is(err, ErrServiceTypeMismatch) {
		t.Fatalf("错误内置服务类型应被拒绝，实际为 %v", err)
	}
	resolved, err = app.Make(serviceKeyConfig)
	if err != nil || resolved != replacement || app.Config() != replacement {
		t.Fatalf("类型拒绝后不应出现半更新: resolved=%p facade=%p err=%v", resolved, app.Config(), err)
	}

	factoryCalled := false
	if err = container.TryBind(serviceKeyConfig, func() (*config.Config, error) {
		factoryCalled = true
		return nil, errors.New("factory failed")
	}); !errors.Is(err, ErrProtectedServiceMutation) {
		t.Fatalf("内置服务工厂重绑应被拒绝，实际为 %v", err)
	}
	if factoryCalled {
		t.Fatal("被拒绝的内置服务工厂不应执行")
	}
	if err = container.TryDelete(serviceKeyConfig); !errors.Is(err, ErrProtectedServiceMutation) {
		t.Fatalf("删除内置服务应被拒绝，实际为 %v", err)
	}
	resolved, err = app.Make(serviceKeyConfig)
	if err != nil || resolved != replacement || app.Config() != replacement {
		t.Fatalf("拒绝工厂或删除后不应出现半更新: resolved=%p facade=%p err=%v", resolved, app.Config(), err)
	}
	if original == replacement {
		t.Fatal("测试前置条件错误：替换配置必须是新实例")
	}

	otherContainer := NewContainer()
	if err = container.TryInstance(serviceKeyContainer, otherContainer); !errors.Is(err, ErrProtectedServiceMutation) {
		t.Fatalf("应用容器身份替换应被拒绝，实际为 %v", err)
	}
	if app.Container() != container {
		t.Fatal("应用容器身份在拒绝后发生变化")
	}
}

// TestApplicationContainerMutationRaceWithRunLease 验证并发冻结只有“冻结前提交”或“冻结后拒绝”两种完整结果。
func TestApplicationContainerMutationRaceWithRunLease(t *testing.T) {
	app := &App{container: NewContainer(), lifecycle: appLifecycle{state: ApplicationStateConstructed}}
	container := app.Container()
	start := make(chan struct{})
	results := make(chan error, 64)
	for index := 0; index < cap(results); index++ {
		index := index
		go func() {
			<-start
			results <- container.TryInstance("coherence.concurrent", index)
		}()
	}
	close(start)
	lease, err := app.AcquireRunLease()
	if err != nil {
		t.Fatalf("获取运行租约失败: %v", err)
	}
	for index := 0; index < cap(results); index++ {
		mutationErr := <-results
		if mutationErr != nil && !errors.Is(mutationErr, ErrApplicationRunning) {
			t.Fatalf("并发变更返回了非预期错误: %v", mutationErr)
		}
	}
	if err = container.TryInstance("coherence.after-freeze", true); !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("租约建立后不得再提交变更，实际为 %v", err)
	}
	lease.Release()
	if err = app.Close(); err != nil {
		t.Fatalf("关闭并发测试应用失败: %v", err)
	}
}

// TestStandaloneContainerMutationCompatibility 验证独立容器保留原有无门禁行为并提供可选错误返回接口。
func TestStandaloneContainerMutationCompatibility(t *testing.T) {
	container := NewContainer()
	if err := container.TryBind("singleton", "ready"); err != nil {
		t.Fatalf("独立容器 TryBind 失败: %v", err)
	}
	container.BindFactory("factory", func() string { return "factory" })
	container.BindScoped("scoped", func() string { return "scoped" })
	container.Instance("instance", "explicit")
	if value, err := container.Make("singleton"); err != nil || value != "ready" {
		t.Fatalf("独立容器单例行为发生变化: value=%#v err=%v", value, err)
	}
	if err := container.TryDelete("singleton"); err != nil || container.Has("singleton") {
		t.Fatalf("独立容器删除行为发生变化: has=%v err=%v", container.Has("singleton"), err)
	}
}

func assertContainerMutationPanic(t *testing.T, target error, action func()) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatalf("无返回值容器变更被拒绝时必须 panic: target=%v", target)
		}
		err, ok := recovered.(error)
		if !ok || !errors.Is(err, target) {
			t.Fatalf("容器变更 panic 错误不稳定: panic=%#v target=%v", recovered, target)
		}
	}()
	action()
}
