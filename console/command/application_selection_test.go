package command

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// newCommandMultiApplicationProject 构造两个真实编译期应用，供控制台命令验证
// 默认应用、显式选择及全应用执行语义。
func newCommandMultiApplicationProject(t *testing.T) *framework.App {
	t.Helper()
	basePath := t.TempDir()
	ensureConsoleTestConfigFiles(t, basePath)
	for _, name := range []string{"index", "admin"} {
		if err := os.MkdirAll(filepath.Join(basePath, "app", name), 0o755); err != nil {
			t.Fatalf("创建命令测试应用目录 %q 失败: %v", name, err)
		}
	}
	project := framework.NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = project.Close() })
	if err := project.Config().Set("app.default_app", "admin"); err != nil {
		t.Fatalf("设置命令测试默认应用失败: %v", err)
	}
	definition := func(name string) framework.ApplicationDefinition {
		return framework.ApplicationDefinition{Name: name, Register: func(current *framework.App) error {
			if err := current.Config().Set("app.command_marker", name); err != nil {
				return err
			}
			return current.RegisterRouteLoader(func(routeApplication *framework.App) error {
				routeApplication.Route().Get("/"+name+"-only", func() string { return name })
				return nil
			})
		}}
	}
	if err := project.RegisterApplications(
		func(*framework.App) error { return nil },
		definition("index"),
		definition("admin"),
	); err != nil {
		t.Fatalf("注册命令测试多应用失败: %v", err)
	}
	if err := project.Initialize(); err != nil {
		t.Fatalf("初始化命令测试项目失败: %v", err)
	}
	if err := project.BootProviders(); err != nil {
		t.Fatalf("启动命令测试项目服务失败: %v", err)
	}
	return project
}

// TestApplicationAwareReadCommandsUseSelectedApplication 验证路由和配置读取命令
// 都从 --app 指定的独立容器读取，不会继续使用入口 index 的状态。
func TestApplicationAwareReadCommandsUseSelectedApplication(t *testing.T) {
	project := newCommandMultiApplicationProject(t)
	stdout := &bytes.Buffer{}
	output := console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)

	routes := &RouteList{Command: console.Command{App: project}}
	routeInput := parseMigrationCommandInput(t, routes, "--app", "admin")
	if err := routes.Execute(routeInput, output); err != nil {
		t.Fatalf("列出 admin 路由失败: %v", err)
	}
	if !strings.Contains(stdout.String(), "/admin-only") || strings.Contains(stdout.String(), "/index-only") {
		t.Fatalf("route:list 未隔离 admin 路由: %q", stdout.String())
	}

	stdout.Reset()
	dump := &ConfigDump{Command: console.Command{App: project}}
	dumpInput := parseMigrationCommandInput(t, dump, "app.command_marker", "--app", "admin")
	if err := dump.Execute(dumpInput, output); err != nil {
		t.Fatalf("导出 admin 配置失败: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != `"admin"` {
		t.Fatalf("config:dump 未读取 admin 配置: %q", stdout.String())
	}
}

// TestCompiledApplicationsForCommandSelectsDefaultAndAll 验证未指定 --app
// 时选择 default_app，部署类命令可稳定遍历全部已编译应用。
func TestCompiledApplicationsForCommandSelectsDefaultAndAll(t *testing.T) {
	project := newCommandMultiApplicationProject(t)
	selected, err := compiledApplicationsForCommand(project, "", false)
	if err != nil {
		t.Fatalf("选择默认控制台应用失败: %v", err)
	}
	if len(selected) != 1 || selected[0].name != "admin" || !selected[0].application.Initialized() {
		t.Fatalf("默认控制台应用选择错误: %#v", selected)
	}
	if marker := selected[0].application.Config().GetString("app.command_marker"); marker != "admin" {
		t.Fatalf("默认应用配置未隔离: %q", marker)
	}

	all, err := compiledApplicationsForCommand(project, "", true)
	if err != nil {
		t.Fatalf("选择全部控制台应用失败: %v", err)
	}
	names := make([]string, 0, len(all))
	for _, current := range all {
		names = append(names, current.name)
	}
	if !reflect.DeepEqual(names, []string{"admin", "index"}) {
		t.Fatalf("全部控制台应用顺序错误: %v", names)
	}
	if _, err := compiledApplicationsForCommand(project, "missing", false); err == nil {
		t.Fatal("未编译的 --app 必须被拒绝")
	}
}
