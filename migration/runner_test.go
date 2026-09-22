package migration

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

const (
	firstChecksum  = "1111111111111111111111111111111111111111111111111111111111111111"
	secondChecksum = "2222222222222222222222222222222222222222222222222222222222222222"
)

type migrationStoreProbe struct {
	applied []History
	steps   []string
	failOn  string
}

func (store *migrationStoreProbe) Ensure(context.Context) error {
	store.steps = append(store.steps, "ensure")
	return nil
}

func (store *migrationStoreProbe) Applied(context.Context) ([]History, error) {
	return append([]History(nil), store.applied...), nil
}

func (store *migrationStoreProbe) Inspect(context.Context) ([]History, bool, error) {
	return append([]History(nil), store.applied...), true, nil
}

func (store *migrationStoreProbe) Apply(ctx context.Context, current Migration, batch int64) error {
	if store.failOn == current.Name() {
		return errors.New("apply failed")
	}
	if err := current.Up(ctx, nil); err != nil {
		return err
	}
	store.steps = append(store.steps, "up:"+current.Name())
	store.applied = append(store.applied, History{Name: current.Name(), Checksum: current.Checksum(), Batch: batch})
	return nil
}

func (store *migrationStoreProbe) Revert(ctx context.Context, current Migration) error {
	if err := current.Down(ctx, nil); err != nil {
		return err
	}
	store.steps = append(store.steps, "down:"+current.Name())
	for index, applied := range store.applied {
		if applied.Name == current.Name() {
			store.applied = append(store.applied[:index], store.applied[index+1:]...)
			break
		}
	}
	return nil
}

func mustMigration(t *testing.T, name, checksum string) Migration {
	t.Helper()
	current, err := New(name, checksum, func(context.Context, Executor) error { return nil }, func(context.Context, Executor) error { return nil })
	if err != nil {
		t.Fatalf("创建迁移失败: %v", err)
	}
	return current
}

// TestRunnerAppliesAndRollsBackInStableOrder 验证迁移按名称升序执行、共享批次，并按应用逆序回滚。
func TestRunnerAppliesAndRollsBackInStableOrder(t *testing.T) {
	registry := NewRegistry()
	second := mustMigration(t, "202608150002_create_orders", secondChecksum)
	first := mustMigration(t, "202608150001_create_users", firstChecksum)
	if err := registry.Register(second); err != nil {
		t.Fatalf("注册第二个迁移失败: %v", err)
	}
	if err := registry.Register(first); err != nil {
		t.Fatalf("注册第一个迁移失败: %v", err)
	}
	store := &migrationStoreProbe{}
	runner, err := NewRunner(registry, store)
	if err != nil {
		t.Fatalf("创建迁移运行器失败: %v", err)
	}
	result, err := runner.Apply(context.Background())
	if err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}
	if result.Batch != 1 || !reflect.DeepEqual(result.Names, []string{first.Name(), second.Name()}) {
		t.Fatalf("迁移执行结果错误: %#v", result)
	}
	if !reflect.DeepEqual(store.steps, []string{"ensure", "up:" + first.Name(), "up:" + second.Name()}) {
		t.Fatalf("迁移执行顺序错误: %v", store.steps)
	}

	rollback, err := runner.Rollback(context.Background(), 1)
	if err != nil {
		t.Fatalf("回滚最近批次失败: %v", err)
	}
	if !reflect.DeepEqual(rollback.Names, []string{second.Name(), first.Name()}) {
		t.Fatalf("迁移回滚顺序错误: %#v", rollback)
	}
}

// TestRunnerRejectsUnboundedRollbackBatches 验证公共 API 在分配批次集合前拒绝异常大的批次数。
func TestRunnerRejectsUnboundedRollbackBatches(t *testing.T) {
	store := &migrationStoreProbe{}
	runner, err := NewRunner(NewRegistry(), store)
	if err != nil {
		t.Fatalf("创建迁移运行器失败: %v", err)
	}
	_, err = runner.Rollback(context.Background(), MaximumRollbackBatches+1)
	if !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("超出批次上限必须返回 ErrInvalidMigration，实际为 %v", err)
	}
	if len(store.steps) != 0 {
		t.Fatalf("非法批次数不得访问迁移存储，实际步骤为 %v", store.steps)
	}
}

// TestRunnerRejectsHistoryDriftAndMissingDefinitions 验证已应用迁移的校验和漂移及定义丢失会阻止继续执行。
func TestRunnerRejectsHistoryDriftAndMissingDefinitions(t *testing.T) {
	registry := NewRegistry()
	current := mustMigration(t, "202608150001_create_users", firstChecksum)
	if err := registry.Register(current); err != nil {
		t.Fatalf("注册迁移失败: %v", err)
	}
	for _, testCase := range []struct {
		name    string
		history History
		want    error
	}{
		{name: "checksum_drift", history: History{Name: current.Name(), Checksum: secondChecksum, Batch: 1}, want: ErrMigrationDrift},
		{name: "missing_definition", history: History{Name: "202608140001_removed", Checksum: firstChecksum, Batch: 1}, want: ErrMigrationMissing},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runner, err := NewRunner(registry, &migrationStoreProbe{applied: []History{testCase.history}})
			if err != nil {
				t.Fatalf("创建迁移运行器失败: %v", err)
			}
			if _, err := runner.Apply(context.Background()); !errors.Is(err, testCase.want) {
				t.Fatalf("迁移历史异常必须被拒绝，实际为 %v", err)
			}
		})
	}
}

// TestRegistryRejectsInvalidAndDuplicateMigrations 验证迁移名称、校验和与唯一性契约。
func TestRegistryRejectsInvalidAndDuplicateMigrations(t *testing.T) {
	if _, err := New("bad", firstChecksum, func(context.Context, Executor) error { return nil }, nil); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("非法迁移名称必须被拒绝，实际为 %v", err)
	}
	if _, err := New("202608150001_valid", "short", func(context.Context, Executor) error { return nil }, nil); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("非法迁移校验和必须被拒绝，实际为 %v", err)
	}
	registry := NewRegistry()
	current := mustMigration(t, "202608150001_valid", firstChecksum)
	if err := registry.Register(current); err != nil {
		t.Fatalf("注册迁移失败: %v", err)
	}
	if err := registry.Register(current); !errors.Is(err, ErrDuplicateMigration) {
		t.Fatalf("重复迁移必须被拒绝，实际为 %v", err)
	}
}
