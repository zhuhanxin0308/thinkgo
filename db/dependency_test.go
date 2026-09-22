package db_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestDatabaseCoreDoesNotImportOptionalBackends 验证只使用模型和 SQL 时不会编译 NoSQL 驱动。
func TestDatabaseCoreDoesNotImportOptionalBackends(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("查询编译依赖失败: %v %s", err, output)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if strings.HasPrefix(dependency, "go.mongodb.org/") || strings.HasPrefix(dependency, "github.com/neo4j/") {
			t.Fatalf("数据库核心包含可选后端: %s", dependency)
		}
	}
}
