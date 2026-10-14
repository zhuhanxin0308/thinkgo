package migration

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// NewSQL 创建由完整 SQL 语句序列组成的迁移，并根据双向语句自动计算稳定校验和。
// 每个元素必须是一条可由驱动独立执行的完整语句，框架不会按分号猜测拆分 SQL。
// SQL 必须来自可信代码；校验和不是作者签名，也不判断语句是否安全。
// 需要在首次应用前核对独立审查清单时，使用 NewSQLWithApproval 和 SQLApprovalDigest；
// 不将本迁移的历史 Checksum 值用作执行批准摘要。
// 普通定义保留既有摘要；含完整语句 "down" 的定义改用无歧义的 v2 编码。
// 此类旧历史会报告 ErrMigrationDrift，不回退接受旧摘要或自动重写记录；
// 升级限制见 docs/security/migration-history-checksums.md。
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

// up/down 已由 normalizeStatements 拒绝 NUL 并去除首尾空白。
// 无完整 "down" 语句时，旧编码只有一个方向分隔标记，可以唯一解码。
// 否则不能保留旧摘要：即使它与历史相等，也不能证明原来的方向边界。
func checksumStatements(up, down []string) string {
	for _, statements := range [][]string{up, down} {
		for _, statement := range statements {
			if statement == "down" {
				return checksumFramedStatements(up, down)
			}
		}
	}
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

// checksumFramedStatements 的用途域、方向、数量及字节长度固定编码为 v2。
// 与旧编码的 "up\x00" 前缀分离；存储结果仍为 64 位小写 SHA-256。
// 版本在哈希输入中，不是数据库新字段，也不是可信升级旧记录的依据。
func checksumFramedStatements(up, down []string) string {
	hash := sha256.New()
	writeUint := func(value uint64) {
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], value)
		_, _ = hash.Write(encoded[:])
	}
	writeText := func(value string) {
		writeUint(uint64(len(value)))
		_, _ = io.WriteString(hash, value)
	}
	writeText("thinkgo/sql-migration-history/v2")
	for _, group := range []struct {
		direction  string
		statements []string
	}{{"up", up}, {"down", down}} {
		writeText(group.direction)
		writeUint(uint64(len(group.statements)))
		for _, statement := range group.statements {
			writeText(statement)
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
