package migration

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

var (
	// ErrInvalidMigration 表示迁移定义、名称、校验和或上下文非法。
	ErrInvalidMigration = errors.New("invalid migration")
	// ErrDuplicateMigration 表示同名迁移被重复注册。
	ErrDuplicateMigration = errors.New("duplicate migration")
	// ErrMigrationRegistryFrozen 表示迁移注册表已进入只读执行阶段。
	ErrMigrationRegistryFrozen = errors.New("migration registry is frozen")
	// ErrMigrationDrift 表示已应用迁移的校验和与当前代码不同。
	ErrMigrationDrift = errors.New("applied migration checksum drift")
	// ErrMigrationMissing 表示数据库历史中的迁移在当前代码中不存在。
	ErrMigrationMissing = errors.New("applied migration definition missing")
	// ErrIrreversibleMigration 表示迁移没有定义回滚操作。
	ErrIrreversibleMigration = errors.New("migration is irreversible")
	// ErrMigrationLockUnavailable 表示无法取得数据库级迁移会话锁。
	ErrMigrationLockUnavailable = errors.New("migration lock unavailable")
	// ErrMigrationLockLost 表示当前持有者已经不能证明自己仍拥有迁移锁或最新 fencing token。
	ErrMigrationLockLost = errors.New("migration lock lost")
	// ErrMigrationLockRequired 表示数据库迁移写入绕过了受保护的固定会话。
	ErrMigrationLockRequired = errors.New("migration lock required")
	// ErrMigrationDirty 表示上一次迁移没有到达可证明的最终状态，需要人工核验后显式恢复。
	ErrMigrationDirty = errors.New("migration state is dirty")
	// ErrMigrationNotDirty 表示恢复目标已经处于明确的最终状态。
	ErrMigrationNotDirty = errors.New("migration state is not dirty")
)

var (
	migrationNamePattern     = regexp.MustCompile(`^[0-9]{12,20}_[a-z][a-z0-9_]{0,127}$`)
	migrationChecksumPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// Executor 是迁移所需的可取消原生数据库执行契约，由 DB 和事务实现。
type Executor interface {
	QueryContext(context.Context, string, ...interface{}) ([]map[string]interface{}, error)
	ExecuteContext(context.Context, string, ...interface{}) (int64, error)
}

// Migration 是一个具有稳定名称、内容校验和和双向操作的数据库变更。
type Migration interface {
	Name() string
	Checksum() string
	Up(context.Context, Executor) error
	Down(context.Context, Executor) error
}

type definition struct {
	name     string
	checksum string
	up       func(context.Context, Executor) error
	down     func(context.Context, Executor) error
}

// New 创建经过完整契约校验的程序化迁移。
func New(
	name string,
	checksum string,
	up func(context.Context, Executor) error,
	down func(context.Context, Executor) error,
) (Migration, error) {
	if !migrationNamePattern.MatchString(name) {
		return nil, fmt.Errorf("%w: 名称 %q 必须由时间序列、下划线和小写标识组成", ErrInvalidMigration, name)
	}
	if !migrationChecksumPattern.MatchString(checksum) {
		return nil, fmt.Errorf("%w: 迁移 %q 的校验和必须是 64 位小写 SHA-256", ErrInvalidMigration, name)
	}
	if up == nil {
		return nil, fmt.Errorf("%w: 迁移 %q 缺少 Up 操作", ErrInvalidMigration, name)
	}
	return &definition{name: name, checksum: checksum, up: up, down: down}, nil
}

func (migration *definition) Name() string {
	return migration.name
}

func (migration *definition) Checksum() string {
	return migration.checksum
}

func (migration *definition) Up(ctx context.Context, executor Executor) error {
	if ctx == nil {
		return fmt.Errorf("%w: 迁移 %q 的上下文为空", ErrInvalidMigration, migration.name)
	}
	return migration.up(ctx, executor)
}

func (migration *definition) Down(ctx context.Context, executor Executor) error {
	if ctx == nil {
		return fmt.Errorf("%w: 迁移 %q 的上下文为空", ErrInvalidMigration, migration.name)
	}
	if migration.down == nil {
		return fmt.Errorf("%w: %s", ErrIrreversibleMigration, migration.name)
	}
	return migration.down(ctx, executor)
}
