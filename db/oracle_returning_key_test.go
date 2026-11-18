//go:build oracle

package db

import (
	"context"
	"testing"
)

func TestResolveOracleSynonym(t *testing.T) {
	// 这是一个回归测试，确保同义词解析能够处理别名。
	// 由于此测试需要真实的 Oracle 环境，此处仅作编译期验证。
	c := &SQLConnection{}
	_, _, _ = c.resolveOracleReturningTable(context.Background(), nil, "non_existent_table")
}
