package command

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
)

// TestApplicationAssemblySharesRootController 验证公共基类可跨应用复用且不会引入循环导入。
func TestApplicationAssemblySharesRootController(t *testing.T) {
	base := t.TempDir()
	sources, err := projectSources("example.com/shared")
	if err != nil {
		t.Fatal(err)
	}
	for path, source := range sources {
		writeDiscoveryFixture(t, base, path, string(source))
	}
	writeDiscoveryModuleFixture(t, base, "example.com/shared")
	writeDiscoveryFixture(t, base, "external_test.go", "package main_test\nimport . \"example.com/shared/app\"\nvar _ BaseController\nfunc registerApplications() {}\n")
	writeDiscoveryFixture(t, base, "app/base_controller.go", `package app
import framework "github.com/zhuhanxin0308/thinkgo/v3"
type BaseController struct { framework.Controller; initialized bool }
func (c *BaseController) Initialize() { c.initialized = c.App != nil && c.Request != nil }
func (c *BaseController) Shared() string {
    if !c.initialized { return "缺少请求注入或初始化" }
    return c.App.CurrentApplicationName() + ":" + c.Request.Header("X-Probe")
}
`)
	for _, name := range []string{"index", "admin"} {
		for path, source := range nativeApplicationBuildSources(name) {
			writeDiscoveryFixture(t, base, path, source)
		}
		writeDiscoveryFixture(t, base, "app/"+name+"/controller/base_controller.go", `package controller

import application "example.com/shared/app"

type BaseController struct { application.BaseController }
`)
		writeDiscoveryFixture(t, base, "app/"+name+"/controller/probe.go", `package controller
type Probe struct { BaseController }
func (c *Probe) Index() string { return c.Shared() }
`)
		writeDiscoveryFixture(t, base, "app/"+name+"/route/app.go", `package route
import framework "github.com/zhuhanxin0308/thinkgo/v3"
func Load(route *framework.Route) {
    route.Get("/", "Index@Index")
    route.Get("hello/:name", "Index@Hello")
    route.Get("probe", "Probe@Index")
}
`)
	}
	writeDiscoveryFixture(t, base, "assembly_test.go", `package main
import (
    "net/http"
    "net/http/httptest"
    "testing"
    framework "github.com/zhuhanxin0308/thinkgo/v3"
    fwhttp "github.com/zhuhanxin0308/thinkgo/v3/http"
)
func TestSharedControllerLifecycle(t *testing.T) {
    app := framework.NewConsoleAppUninitialized(".")
    t.Cleanup(func() { _ = app.Close() })
    if err := registerApplications(app); err != nil { t.Fatal(err) }
    if err := app.Initialize(); err != nil { t.Fatal(err) }
    host, err := fwhttp.NewHttp(app)
    if err != nil { t.Fatal(err) }
    for _, name := range []string{"index", "admin"} {
        for _, value := range []string{"first", "second"} {
            request := httptest.NewRequest(http.MethodGet, "http://localhost/"+name+"/probe", nil)
            request.Header.Set("X-Probe", value)
            response := httptest.NewRecorder()
            host.ServeHTTP(response, request)
            if response.Code != http.StatusOK || response.Body.String() != name+":"+value {
                t.Fatalf("跨应用请求隔离或生命周期错误: %d %s", response.Code, response.Body.String())
            }
        }
    }
}
`)
	writeDiscoveryFixture(t, base, "app/admin/command/probe.go", `package command
import "github.com/zhuhanxin0308/thinkgo/v3/console"
type Probe struct { console.Command }
func (c *Probe) Configure() { c.Signature = "shared:probe"; c.Description = "共享应用探测" }
func (c *Probe) RequiresDatabase() bool { return false }
func (c *Probe) Execute(_ *console.Input, output *console.Output) error {
    output.Writeln("共享命令正常")
    return nil
}
`)
	app := &framework.App{BasePath: base}
	if err := RefreshControllerDiscovery(app); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"test", "-mod=mod", "./...", "-count=1"},
		{"run", "-mod=readonly", "./cmd/think", "service:discover"},
		{"run", "-mod=readonly", "./cmd/think", "route:list", "--app", "admin"},
		{"run", "-mod=readonly", "./cmd/think", "shared:probe"},
		{"run", "-mod=readonly", "-tags=thinkgo_runtime", "./cmd/think", "shared:probe"},
		{"build", "-mod=readonly", "-o", filepath.Join(t.TempDir(), "http.exe"), "."},
	} {
		process := exec.Command("go", args...)
		process.Dir = base
		process.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
		output, err := process.CombinedOutput()
		if err != nil {
			t.Fatalf("公共基类的下游验收 %v 失败: %v\n%s", args, err, output)
		}
		if args[len(args)-1] == "shared:probe" && !strings.Contains(string(output), "共享命令正常") {
			t.Fatalf("自定义命令未执行: %s", output)
		}
	}
	if err := CheckControllerDiscovery(app); err != nil {
		t.Fatalf("重复生成后清单不一致: %v", err)
	}
	// 模拟旧版 HTTP、运行时 CLI 和入口测试，使用真实源码命令完成迁移。
	for _, path := range []string{"main.go", "main_test.go", "cmd/think/runtime.go"} {
		content := readGeneratedDiscoverySource(t, base, path)
		content = strings.ReplaceAll(content, "registerApplications", "businessapp.Register")
		content = strings.Replace(content, "import (", "import (\n businessapp \"example.com/shared/app\"", 1)
		writeDiscoveryFixture(t, base, path, content)
	}
	writeDiscoveryFixture(t, base, "app/autoload_generated.go", `package app
import (
    framework "github.com/zhuhanxin0308/thinkgo/v3"
    "example.com/shared/app/index"
    "example.com/shared/app/admin"
)
func Register(app *framework.App) error {
    return app.RegisterApplications(func(current *framework.App) error {
        return current.RegisterGlobalComponents(framework.GlobalApplicationComponents{
            Services: Services(), Events: Events(), Middleware: Middleware(), Providers: Providers(),
        })
    }, index.Definition(), admin.Definition())
}
`)
	for _, args := range [][]string{
		{"run", "-mod=readonly", "./cmd/think", "service:discover", "--migrate"},
		{"test", "-mod=readonly", "./...", "-count=1"},
		{"run", "-mod=readonly", "-tags=thinkgo_runtime", "./cmd/think", "shared:probe"},
	} {
		process := exec.Command("go", args...)
		process.Dir = base
		process.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
		if output, err := process.CombinedOutput(); err != nil {
			t.Fatalf("真实旧项目迁移验收 %v 失败: %v\n%s", args, err, output)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "internal", "appregistry")); !os.IsNotExist(err) {
		t.Fatalf("不应创建项目注册包: %v", err)
	}
}
