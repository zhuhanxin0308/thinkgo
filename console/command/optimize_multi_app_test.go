package command

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

type optimizeIndexModel struct{}
type optimizeAdminModel struct{}

// TestOptimizeCommandsDiscoverAllNativeApplications 验证未指定 dir 时，配置、
// 路由和模型优化按 ThinkPHP 多应用模式处理根配置及全部编译应用。
func TestOptimizeCommandsDiscoverAllNativeApplications(t *testing.T) {
	basePath := t.TempDir()
	ensureConsoleTestConfigFiles(t, basePath)
	writeDiscoveryFixture(t, basePath, "app/index/config/feature.json", `{"name":"index"}`)
	writeDiscoveryFixture(t, basePath, "app/admin/config/feature.json", `{"name":"admin"}`)
	writeDiscoveryFixture(t, basePath, "app/index/route/app.go", "package route\n")
	writeDiscoveryFixture(t, basePath, "app/admin/route/app.go", "package route\n")
	writeDiscoveryFixture(t, basePath, "app/index/model/user.go", "package model\n\ntype User struct{}\n")
	writeDiscoveryFixture(t, basePath, "app/admin/model/user.go", "package model\n\ntype User struct{}\n")

	application := framework.NewConsoleAppUninitialized(basePath)
	if err := application.RegisterApplications(
		func(*framework.App) error { return nil },
		framework.ApplicationDefinition{Name: "index", Register: optimizeApplicationLoader("index", &optimizeIndexModel{})},
		framework.ApplicationDefinition{Name: "admin", Register: optimizeApplicationLoader("admin", &optimizeAdminModel{})},
	); err != nil {
		t.Fatalf("注册优化测试应用失败: %v", err)
	}
	if err := application.Initialize(); err != nil {
		t.Fatalf("初始化优化测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)

	configCommand := &OptimizeConfig{Command: console.Command{App: application}}
	if err := configCommand.Execute(console.NewInput(), output); err != nil {
		t.Fatalf("优化全部应用配置失败: %v", err)
	}
	globalConfig := readOptimizeCacheFile(t, basePath, "runtime/config.json")
	indexConfig := readOptimizeCacheFile(t, basePath, "runtime/index/config.json")
	adminConfig := readOptimizeCacheFile(t, basePath, "runtime/admin/config.json")
	if bytes.Contains(globalConfig, []byte(`"feature"`)) {
		t.Fatalf("根配置缓存被 index 应用配置污染:\n%s", globalConfig)
	}
	if !bytes.Contains(indexConfig, []byte(`"index"`)) || bytes.Contains(indexConfig, []byte(`"admin"`)) {
		t.Fatalf("index 配置缓存错误:\n%s", indexConfig)
	}
	if !bytes.Contains(adminConfig, []byte(`"admin"`)) || bytes.Contains(adminConfig, []byte(`"index"`)) {
		t.Fatalf("admin 配置缓存错误:\n%s", adminConfig)
	}

	routeCommand := &RouteExport{Command: console.Command{App: application}}
	if err := routeCommand.Execute(console.NewInput(), output); err != nil {
		t.Fatalf("优化全部应用路由失败: %v", err)
	}
	indexRoutes := readOptimizeCacheFile(t, basePath, "runtime/index/route.json")
	adminRoutes := readOptimizeCacheFile(t, basePath, "runtime/admin/route.json")
	if !bytes.Contains(indexRoutes, []byte(`/index-only`)) || bytes.Contains(indexRoutes, []byte(`/admin-only`)) {
		t.Fatalf("index 路由缓存错误:\n%s", indexRoutes)
	}
	if !bytes.Contains(adminRoutes, []byte(`/admin-only`)) || bytes.Contains(adminRoutes, []byte(`/index-only`)) {
		t.Fatalf("admin 路由缓存错误:\n%s", adminRoutes)
	}

	schemaCommand := &SchemaValidate{Command: console.Command{App: application}}
	if err := schemaCommand.Execute(console.NewInput(), output); err != nil {
		t.Fatalf("预热全部应用模型失败: %v", err)
	}
	if err := schemaCommand.Execute(console.NewInput("admin"), output); err != nil {
		t.Fatalf("预热指定 admin 应用模型失败: %v", err)
	}
	if err := schemaCommand.Execute(console.NewInput("missing"), output); err == nil {
		t.Fatal("未编译应用的模型预热必须失败")
	}
}

func optimizeApplicationLoader(name string, model interface{}) framework.ApplicationLoader {
	return func(current *framework.App) error {
		if err := current.RegisterModel("User", model); err != nil {
			return err
		}
		return current.RegisterRouteLoader(func(routeApplication *framework.App) error {
			routeApplication.Route().Get("/"+name+"-only", name+"/index")
			return nil
		})
	}
}

func readOptimizeCacheFile(t *testing.T, basePath, relativePath string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(basePath, filepath.FromSlash(relativePath)))
	if err != nil {
		t.Fatalf("读取优化缓存 %q 失败: %v", relativePath, err)
	}
	return content
}
