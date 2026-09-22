package framework

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRequestTasksBoundAdmissionAndCloseRetry 验证积压上限、关闭超时和重试时的依赖存活边界。
func TestRequestTasksBoundAdmissionAndCloseRetry(t *testing.T) {
	app := &App{}
	task, err := app.AcquireRequestTask(1, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.AcquireRequestTask(1, time.Second); !errors.Is(err, ErrRequestTaskCapacity) {
		t.Fatalf("满载必须拒绝新任务: %v", err)
	}
	if err = app.Close(); !errors.Is(err, ErrRequestTasksPending) {
		t.Fatalf("未完成任务必须阻止关闭依赖: %v", err)
	}
	if app.State() == ApplicationStateClosed {
		t.Fatal("任务仍在使用依赖时不能标记关闭")
	}
	if _, err = app.AcquireRequestTask(1, time.Second); !errors.Is(err, ErrApplicationClosed) {
		t.Fatalf("开始停机后不得再接受任务: %v", err)
	}
	task.Release()
	task.Release()
	if err = app.Close(); err != nil {
		t.Fatalf("任务结束后关闭必须可以重试: %v", err)
	}
	snapshot := app.RequestTaskSnapshot()
	if snapshot.Active != 0 || snapshot.Completed != 1 || snapshot.Rejected != 2 || snapshot.ShutdownTimeouts != 1 {
		t.Fatalf("任务观测计数错误: %+v", snapshot)
	}
}

// TestWaitRequestTasksRespectsContextAndDoesNotCloseAdmission 验证运行期排空屏障不改变服务接收状态。
func TestWaitRequestTasksRespectsContextAndDoesNotCloseAdmission(t *testing.T) {
	app := &App{}
	task, err := app.AcquireRequestTask(2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = app.WaitRequestTasks(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("等待必须保留上下文错误: %v", err)
	}
	second, err := app.AcquireRequestTask(2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	task.Release()
	second.Release()
	if err = app.WaitRequestTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = app.AcquireRequestTask(0, time.Second); err == nil {
		t.Fatal("非法容量必须被拒绝")
	}
}

// TestOwnedApplicationTasksProtectRootClose 验证子应用遗留任务不会耗尽根应用的关闭重试机会。
func TestOwnedApplicationTasksProtectRootClose(t *testing.T) {
	child := &App{}
	root := &App{applicationCatalog: &applicationCatalog{
		names: []string{"child"}, applications: map[string]*App{"child": child},
	}}
	task, err := child.AcquireRequestTask(1, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Release()
	if err := root.Close(); !errors.Is(err, ErrRequestTasksPending) {
		t.Fatalf("子应用收尾必须阻止根应用关闭: %v", err)
	}
	if root.State() == ApplicationStateClosed || child.State() == ApplicationStateClosed {
		t.Fatal("应用集合排空失败时必须保留全部依赖")
	}
	task.Release()
	if err := root.Close(); err != nil {
		t.Fatalf("子应用完成后整个应用集合必须能重新关闭: %v", err)
	}
	if root.State() != ApplicationStateClosed || child.State() != ApplicationStateClosed {
		t.Fatal("重试必须关闭根应用和子应用")
	}
}

// TestClosedApplicationCannotBuildUnbuiltCatalog 验证停机快照之后不能首次构造一批新的子应用。
func TestClosedApplicationCannotBuildUnbuiltCatalog(t *testing.T) {
	root := NewConsoleAppUninitialized(t.TempDir())
	if err := os.MkdirAll(filepath.Join(root.BasePath, "app", "index"), 0o755); err != nil {
		t.Fatal(err)
	}
	loader := func(*App) error { return nil }
	if err := root.RegisterApplications(loader, ApplicationDefinition{Name: "index", Register: loader}); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.BuildApplications(); !errors.Is(err, ErrApplicationClosed) {
		t.Fatalf("停机后未构造的应用目录必须已经封闭: %v", err)
	}
}

// TestCloseTimeoutClosesApplicationRegistration 验证保留依赖的重试窗口不会重新接受应用清单。
func TestCloseTimeoutClosesApplicationRegistration(t *testing.T) {
	root := NewConsoleAppUninitialized(t.TempDir())
	task, err := root.AcquireRequestTask(1, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Release()
	if err := root.Close(); !errors.Is(err, ErrRequestTasksPending) {
		t.Fatal(err)
	}
	loader := func(*App) error { return nil }
	if err := root.RegisterApplications(loader, ApplicationDefinition{Name: "index", Register: loader}); !errors.Is(err, ErrApplicationRegistrationClosed) {
		t.Fatalf("排空开始后不得注册新的应用目录: %v", err)
	}
	task.Release()
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
}
