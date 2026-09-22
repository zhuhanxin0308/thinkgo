package framework

import (
	"context"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/migration"
)

// TestApplicationMigrationRegistriesAreIsolated 验证独立 App 实例的迁移定义互不共享，并通过稳定服务边界解析。
func TestApplicationMigrationRegistriesAreIsolated(t *testing.T) {
	first := NewAppUninitialized(t.TempDir())
	second := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() {
		_ = first.Close()
		_ = second.Close()
	})
	current, err := migration.New(
		"202608150001_create_users",
		"1111111111111111111111111111111111111111111111111111111111111111",
		func(context.Context, migration.Executor) error { return nil },
		func(context.Context, migration.Executor) error { return nil },
	)
	if err != nil {
		t.Fatalf("创建应用迁移失败: %v", err)
	}
	if err := first.RegisterMigration(current); err != nil {
		t.Fatalf("注册第一个应用迁移失败: %v", err)
	}
	firstRegistry, err := ResolveServiceAs[*migration.Registry](first, ServiceMigration)
	if err != nil {
		t.Fatalf("解析第一个应用迁移注册表失败: %v", err)
	}
	secondRegistry, err := ResolveServiceAs[*migration.Registry](second, ServiceMigration)
	if err != nil {
		t.Fatalf("解析第二个应用迁移注册表失败: %v", err)
	}
	if firstRegistry == secondRegistry {
		t.Fatal("独立 App 实例不能共享迁移注册表")
	}
	firstRunner, err := migration.NewRunner(firstRegistry, &emptyMigrationStore{})
	if err != nil {
		t.Fatalf("创建第一个应用迁移运行器失败: %v", err)
	}
	result, err := firstRunner.Apply(context.Background())
	if err != nil || len(result.Names) != 1 {
		t.Fatalf("第一个应用迁移注册表内容错误: result=%#v err=%v", result, err)
	}
	secondRunner, err := migration.NewRunner(secondRegistry, &emptyMigrationStore{})
	if err != nil {
		t.Fatalf("创建第二个应用迁移运行器失败: %v", err)
	}
	result, err = secondRunner.Apply(context.Background())
	if err != nil || len(result.Names) != 0 {
		t.Fatalf("第二个应用不应包含第一个应用迁移: result=%#v err=%v", result, err)
	}
}

type emptyMigrationStore struct{}

func (*emptyMigrationStore) Ensure(context.Context) error { return nil }
func (*emptyMigrationStore) Applied(context.Context) ([]migration.History, error) {
	return nil, nil
}
func (*emptyMigrationStore) Inspect(context.Context) ([]migration.History, bool, error) {
	return nil, true, nil
}
func (*emptyMigrationStore) Apply(_ context.Context, current migration.Migration, _ int64) error {
	return current.Up(context.Background(), nil)
}
func (*emptyMigrationStore) Revert(_ context.Context, current migration.Migration) error {
	return current.Down(context.Background(), nil)
}
