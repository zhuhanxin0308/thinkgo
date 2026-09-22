package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// NewSQL 创建由完整 SQL 语句序列组成的迁移，并根据双向语句自动计算稳定校验和。
// 每个元素必须是一条可由驱动独立执行的完整语句，框架不会按分号猜测拆分 SQL。
func NewSQL(name string, upStatements, downStatements []string) (Migration, error) {
	up, err := normalizeStatements(upStatements)
	if err != nil {
		return nil, fmt.Errorf("%w: 迁移 %q 的 Up 语句无效: %v", ErrInvalidMigration, name, err)
	}
	down, err := normalizeStatements(downStatements)
	if err != nil {
		return nil, fmt.Errorf("%w: 迁移 %q 的 Down 语句无效: %v", ErrInvalidMigration, name, err)
	}
	if len(up) == 0 {
		return nil, fmt.Errorf("%w: 迁移 %q 至少需要一条 Up 语句", ErrInvalidMigration, name)
	}
	checksum := checksumStatements(up, down)
	return New(
		name,
		checksum,
		func(ctx context.Context, executor Executor) error {
			return executeStatements(ctx, executor, up)
		},
		func(ctx context.Context, executor Executor) error {
			if len(down) == 0 {
				return fmt.Errorf("%w: %s", ErrIrreversibleMigration, name)
			}
			return executeStatements(ctx, executor, down)
		},
	)
}

func normalizeStatements(statements []string) ([]string, error) {
	if len(statements) == 0 {
		return nil, nil
	}
	normalized := make([]string, len(statements))
	for index, statement := range statements {
		statement = strings.TrimSpace(statement)
		if statement == "" || strings.IndexByte(statement, 0) >= 0 {
			return nil, fmt.Errorf("第 %d 条语句为空或包含空字节", index+1)
		}
		normalized[index] = statement
	}
	return normalized, nil
}

func checksumStatements(up, down []string) string {
	hash := sha256.New()
	for _, group := range []struct {
		name       string
		statements []string
	}{{name: "up", statements: up}, {name: "down", statements: down}} {
		_, _ = hash.Write([]byte(group.name))
		_, _ = hash.Write([]byte{0})
		for _, statement := range group.statements {
			_, _ = hash.Write([]byte(statement))
			_, _ = hash.Write([]byte{0})
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func executeStatements(ctx context.Context, executor Executor, statements []string) error {
	if ctx == nil || isNilMigration(executor) {
		return fmt.Errorf("%w: SQL 迁移缺少上下文或执行器", ErrInvalidMigration)
	}
	for index, statement := range statements {
		if _, err := executor.ExecuteContext(ctx, statement); err != nil {
			return fmt.Errorf("执行第 %d 条迁移语句失败: %w", index+1, err)
		}
	}
	return nil
}
