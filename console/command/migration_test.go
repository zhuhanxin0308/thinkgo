package command

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/migration"
)

type commandMigrationStore struct {
	history           []migration.History
	honorCancellation bool
}

func (store *commandMigrationStore) contextError(ctx context.Context) error {
	if store.honorCancellation {
		return ctx.Err()
	}
	return nil
}

func (store *commandMigrationStore) Ensure(ctx context.Context) error {
	return store.contextError(ctx)
}
func (store *commandMigrationStore) Applied(ctx context.Context) ([]migration.History, error) {
	if err := store.contextError(ctx); err != nil {
		return nil, err
	}
	return append([]migration.History(nil), store.history...), nil
}
func (store *commandMigrationStore) Inspect(ctx context.Context) ([]migration.History, bool, error) {
	if err := store.contextError(ctx); err != nil {
		return nil, false, err
	}
	return append([]migration.History(nil), store.history...), true, nil
}
func (store *commandMigrationStore) Apply(ctx context.Context, current migration.Migration, batch int64) error {
	if err := store.contextError(ctx); err != nil {
		return err
	}
	if err := current.Up(ctx, nil); err != nil {
		return err
	}
	store.history = append(store.history, migration.History{Name: current.Name(), Checksum: current.Checksum(), Batch: batch})
	return nil
}
func (store *commandMigrationStore) Revert(ctx context.Context, current migration.Migration) error {
	if err := store.contextError(ctx); err != nil {
		return err
	}
	if err := current.Down(ctx, nil); err != nil {
		return err
	}
	store.history = nil
	return nil
}

func newMigrationCommandApp(t *testing.T) *framework.App {
	t.Helper()
	app := framework.NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	current, err := migration.New(
		"202608150001_create_users",
		"1111111111111111111111111111111111111111111111111111111111111111",
		func(context.Context, migration.Executor) error { return nil },
		func(context.Context, migration.Executor) error { return nil },
	)
	if err != nil {
		t.Fatalf("创建命令测试迁移失败: %v", err)
	}
	if err := app.RegisterMigration(current); err != nil {
		t.Fatalf("注册命令测试迁移失败: %v", err)
	}
	app.Instance(string(framework.ServiceDB), db.NewDB(nil))
	return app
}

func parseMigrationCommandInput(t *testing.T, command console.ICommand, args ...string) *console.Input {
	t.Helper()
	command.Configure()
	input := console.NewInput(args...)
	if err := input.Parse(command.GetArgumentDefinitions(), command.GetOptionDefinitions()); err != nil {
		t.Fatalf("解析迁移命令输入失败: %v", err)
	}
	return input
}

func parseMigrationCommandInputContext(t *testing.T, ctx context.Context, command console.ICommand, args ...string) *console.Input {
	t.Helper()
	command.Configure()
	input := console.NewInputContext(ctx, args...)
	if err := input.Parse(command.GetArgumentDefinitions(), command.GetOptionDefinitions()); err != nil {
		t.Fatalf("解析带上下文的迁移命令输入失败: %v", err)
	}
	return input
}

// TestMigrationCommandsApplyReportAndRollback 验证三个命令共享真实迁移历史语义和稳定输出。
func TestMigrationCommandsApplyReportAndRollback(t *testing.T) {
	app := newMigrationCommandApp(t)
	store := &commandMigrationStore{}
	factory := func(*db.DB) (migration.Store, error) { return store, nil }
	stdout := &bytes.Buffer{}
	output := console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)

	apply := &Migrate{Command: console.Command{App: app}, storeFactory: factory}
	if err := apply.Execute(parseMigrationCommandInput(t, apply), output); err != nil {
		t.Fatalf("执行 migrate 失败: %v", err)
	}
	if !strings.Contains(stdout.String(), "Migrated") || len(store.history) != 1 {
		t.Fatalf("migrate 输出或历史错误: output=%q history=%#v", stdout.String(), store.history)
	}

	stdout.Reset()
	status := &MigrateStatus{Command: console.Command{App: app}, storeFactory: factory}
	if err := status.Execute(parseMigrationCommandInput(t, status), output); err != nil {
		t.Fatalf("执行 migrate:status 失败: %v", err)
	}
	if !strings.Contains(stdout.String(), "Applied") || !strings.Contains(stdout.String(), "202608150001_create_users") {
		t.Fatalf("migrate:status 输出错误: %q", stdout.String())
	}

	stdout.Reset()
	rollback := &MigrateRollback{Command: console.Command{App: app}, storeFactory: factory}
	if err := rollback.Execute(parseMigrationCommandInput(t, rollback, "--batches", "1"), output); err != nil {
		t.Fatalf("执行 migrate:rollback 失败: %v", err)
	}
	if !strings.Contains(stdout.String(), "Rolled back") || len(store.history) != 0 {
		t.Fatalf("migrate:rollback 输出或历史错误: output=%q history=%#v", stdout.String(), store.history)
	}
}

// TestMigrationCommandsRejectInvalidInputAndDependencies 验证批次数、应用和存储错误不会被吞掉。
func TestMigrationCommandsRejectInvalidInputAndDependencies(t *testing.T) {
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	rollback := &MigrateRollback{}
	if err := rollback.Execute(parseMigrationCommandInput(t, rollback, "--batches", "0"), output); err == nil {
		t.Fatal("非法回滚批次数必须被拒绝")
	}
	apply := &Migrate{}
	if err := apply.Execute(parseMigrationCommandInput(t, apply), output); !errors.Is(err, framework.ErrNilApplication) {
		t.Fatalf("空应用必须返回 ErrNilApplication，实际为 %v", err)
	}
}

// TestMigrationCommandsPropagateCancellation 验证应用、状态查询和回滚都使用
// 控制台宿主提供的上下文，终止部署进程后不会继续提交数据库变更。
func TestMigrationCommandsPropagateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)

	tests := []struct {
		name    string
		command console.ICommand
		store   *commandMigrationStore
		args    []string
	}{
		{name: "apply", store: &commandMigrationStore{honorCancellation: true}},
		{
			name: "rollback",
			store: &commandMigrationStore{
				honorCancellation: true,
				history: []migration.History{{
					Name: "202608150001_create_users", Checksum: strings.Repeat("1", 64), Batch: 1,
				}},
			},
			args: []string{"--batches", "1"},
		},
		{name: "status", store: &commandMigrationStore{honorCancellation: true}},
	}
	for index := range tests {
		testCase := &tests[index]
		t.Run(testCase.name, func(t *testing.T) {
			app := newMigrationCommandApp(t)
			factory := func(*db.DB) (migration.Store, error) { return testCase.store, nil }
			switch testCase.name {
			case "apply":
				testCase.command = &Migrate{Command: console.Command{App: app}, storeFactory: factory}
			case "rollback":
				testCase.command = &MigrateRollback{Command: console.Command{App: app}, storeFactory: factory}
			case "status":
				testCase.command = &MigrateStatus{Command: console.Command{App: app}, storeFactory: factory}
			}
			err := testCase.command.Execute(parseMigrationCommandInputContext(t, ctx, testCase.command, testCase.args...), output)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("迁移命令必须传播 context.Canceled，实际为 %v", err)
			}
		})
	}
}
