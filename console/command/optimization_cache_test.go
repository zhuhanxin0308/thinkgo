package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

// TestOptimizeRouteCacheConsumedAfterRestart 验证 CLI 产物在下一次 App 生命周期中实际用于 URL 生成。
func TestOptimizeRouteCacheConsumedAfterRestart(t *testing.T) {
	base := t.TempDir()
	ensureConsoleTestConfigFiles(t, base)
	application := buildConsoleTestApp(t, base)
	application.Route().Get("/cached/:id", "Index/read").Name("detail")
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	if err := (&OptimizeRoute{Command: console.Command{App: application}}).Execute(console.NewInput(), output); err != nil {
		t.Fatal(err)
	}
	restarted := buildConsoleTestApp(t, base)
	if err := restarted.LoadRoutes(); err != nil {
		t.Fatal(err)
	}
	if actual, err := restarted.Route().URL("detail", map[string]interface{}{"id": 42}); err != nil || actual != "/cached/42.html" {
		t.Fatalf("重启未使用命名路由缓存: %s %v", actual, err)
	}
	if err := os.WriteFile(filepath.Join(base, "runtime", route.NameCacheFilename), []byte("broken"), 0644); err != nil {
		t.Fatal(err)
	}
	fallback := buildConsoleTestApp(t, base)
	fallback.Route().Get("/live", "Index/index").Name("live")
	if err := fallback.LoadRoutes(); err != nil {
		t.Fatal(err)
	}
	if actual, err := fallback.Route().URL("live", nil); err != nil || actual != "/live.html" {
		t.Fatalf("损坏缓存未回退编译路由: %s %v", actual, err)
	}
}

// TestOptimizeSchemaBoundaries 验证无模型时无需数据库，显式表必须有连接，取消时不得写缓存。
func TestOptimizeSchemaBoundaries(t *testing.T) {
	application := buildConsoleTestApp(t, t.TempDir())
	command := &OptimizeSchema{Command: console.Command{App: application}}
	command.Configure()
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	if err := command.Execute(nil, output); err != nil {
		t.Fatal(err)
	}
	input := console.NewInput("--table", "users", "--connection", "missing")
	if err := input.Parse(command.GetArgumentDefinitions(), command.GetOptionDefinitions()); err != nil {
		t.Fatal(err)
	}
	if err := command.Execute(input, output); err == nil {
		t.Fatal("缺失连接未报告错误")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := command.Execute(console.NewInputContext(ctx), output); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := command.Execute(nil, nil); !errors.Is(err, console.ErrInvalidOutput) {
		t.Fatal(err)
	}
	if err := (&OptimizeSchema{}).Execute(nil, output); !errors.Is(err, framework.ErrNilApplication) {
		t.Fatal(err)
	}
	if err := command.Execute(console.NewInput("../outside"), output); err == nil {
		t.Fatal("目录越界未失败")
	}
}

// TestOptimizeRunsAllAvailableStages 验证总入口确实写出配置与路由缓存，未注册模型不伪造字段结构。
func TestOptimizeRunsAllAvailableStages(t *testing.T) {
	base := t.TempDir()
	application := buildConsoleTestApp(t, base)
	command := &Optimize{Command: console.Command{App: application}}
	if err := command.Execute(console.NewInput(), generatorTestOutput()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", route.NameCacheFilename} {
		content, err := os.ReadFile(filepath.Join(base, "runtime", name))
		if err != nil || !json.Valid(content) {
			t.Fatalf("优化阶段缺少有效产物 %s: %v", name, err)
		}
	}
}
