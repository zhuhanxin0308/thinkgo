package cache_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestCacheCoreDoesNotImportOptionalBackends 验证实际编译依赖，防止错误常量再次把具体后端拉回核心。
func TestCacheCoreDoesNotImportOptionalBackends(t *testing.T) {
	const dependencyCheckTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), dependencyCheckTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("读取编译依赖失败: %v %s", err, output)
	}
	for _, dependency := range strings.Fields(string(output)) {
		for _, backend := range []string{"github.com/redis/", "go.mongodb.org/", "github.com/neo4j/", "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"} {
			if strings.HasPrefix(dependency, backend) {
				t.Fatalf("Cache 核心包含可选后端: %s", dependency)
			}
		}
	}
}
