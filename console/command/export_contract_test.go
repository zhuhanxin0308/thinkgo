package command

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

// TestExportAndConfigFailuresAreErrors 验证不存在的目标不能以警告后成功的方式退出。
func TestExportAndConfigFailuresAreErrors(t *testing.T) {
	app := buildConsoleTestApp(t, t.TempDir())
	for _, command := range []console.ICommand{&OptimizeConfig{Command: console.Command{App: app}}, &RouteExport{Command: console.Command{App: app}}} {
		stdout := &bytes.Buffer{}
		output := console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)
		if err := command.Execute(console.NewInput("missing"), output); err == nil {
			t.Fatal("不存在的目录被报告成功")
		}
		if bytes.Contains(stdout.Bytes(), []byte("Succeed")) {
			t.Fatalf("失败后仍输出成功: %s", stdout.String())
		}
	}
}

// TestOptimizeConfigRejectsInvalidComponent 验证存在但不是目录的应用配置不能被静默跳过。
func TestOptimizeConfigRejectsInvalidComponent(t *testing.T) {
	base := t.TempDir()
	ensureConsoleTestConfigFiles(t, base)
	writeDiscoveryFixture(t, base, "app/index/config", "invalid component")
	app := framework.NewConsoleAppUninitialized(base)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.RegisterApplications(func(*framework.App) error { return nil }, framework.ApplicationDefinition{Name: "index", Register: func(*framework.App) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := &OptimizeConfig{Command: console.Command{App: app}}
	if err := command.Execute(console.NewInput(), console.NewOutputWithWriters(&output, &output, false)); err == nil {
		t.Fatalf("不是目录的配置被忽略: %s %s", filepath.Join(base, "app/index/config"), &output)
	}
}

// TestSchemaValidationHonorsCancellation 验证取消的命令不会尝试创建数据库连接。
func TestSchemaValidationHonorsCancellation(t *testing.T) {
	app := buildConsoleTestApp(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	input := console.NewInputContext(ctx)
	input.Options["table"] = "users"
	command := &SchemaValidate{Command: console.Command{App: app}}
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	if err := command.Execute(input, output); !errors.Is(err, context.Canceled) {
		t.Fatalf("命令取消未传播到表校验: %v", err)
	}
}

// TestRouteExportSupportsActualTypedHandlers 验证导出可识别真实回调和 JSON 契约处理器。
func TestRouteExportSupportsActualTypedHandlers(t *testing.T) {
	callback := func(binding.Input) (string, error) { return "ok", nil }
	wrapped, err := route.NewJSONHandler(callback, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, handler := range []any{callback, wrapped} {
		identity, err := routeHandlerIdentity(handler)
		if err != nil || identity == "" {
			t.Fatalf("真实处理器不可导出: %T %v", handler, err)
		}
	}
}
