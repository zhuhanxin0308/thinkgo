package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
)

// TestRefreshControllerDiscoveryGeneratesNativeApplications 验证服务发现按
// ThinkPHP 多应用目录生成根应用清单和各业务应用自己的装配入口。
func TestRefreshControllerDiscoveryGeneratesNativeApplications(t *testing.T) {
	basePath := t.TempDir()
	writeDiscoveryModuleFixture(t, basePath, "example.com/project")
	writeDiscoveryFixture(t, basePath, "app/service.go", "package app\n\nfunc Services() []interface{} { return nil }\n")
	writeDiscoveryFixture(t, basePath, "app/provider.go", "package app\n\nfunc Providers() map[string]interface{} { return nil }\n")
	writeDiscoveryFixture(t, basePath, "app/event.go", `package app

import framework "github.com/zhuhanxin0308/thinkgo/framework"

func Events() framework.EventDefinition { return framework.EventDefinition{} }
`)
	writeDiscoveryFixture(t, basePath, "app/middleware.go", `package app

import "github.com/zhuhanxin0308/thinkgo/framework/middleware"

func Middleware() []middleware.Handler { return nil }
`)
	writeDiscoveryFixture(t, basePath, "app/index/controller/index.go", "package controller\n\ntype Index struct{}\n")
	writeDiscoveryFixture(t, basePath, "app/index/model/user.go", "package model\n\ntype User struct{}\n")
	writeDiscoveryFixture(t, basePath, "app/index/validate/user.go", "package validate\n\ntype User struct{}\n")
	writeDiscoveryFixture(t, basePath, "app/index/route/app.go", `package route

import framework "github.com/zhuhanxin0308/thinkgo/framework"

func Load(route *framework.Route) {}
`)
	writeDiscoveryFixture(t, basePath, "app/index/event.go", `package index

import framework "github.com/zhuhanxin0308/thinkgo/framework"

func Events() framework.EventDefinition { return framework.EventDefinition{} }
`)
	writeDiscoveryFixture(t, basePath, "app/index/middleware.go", `package index

import "github.com/zhuhanxin0308/thinkgo/framework/middleware"

func Middleware() []middleware.Handler { return nil }
`)
	writeDiscoveryFixture(t, basePath, "app/index/provider.go", "package index\n\nfunc Providers() map[string]interface{} { return nil }\n")
	writeDiscoveryFixture(t, basePath, "app/index/service.go", "package index\n\nfunc Services() []interface{} { return nil }\n")
	writeDiscoveryFixture(t, basePath, "app/admin/controller/user.go", "package controller\n\ntype User struct{}\n")
	writeDiscoveryFixture(t, basePath, "app/admin/route/app.go", `package route

import framework "github.com/zhuhanxin0308/thinkgo/framework"

func Load(route *framework.Route) {}
`)
	for _, emptyDirectory := range []string{"app/controller", "app/view", "app/empty"} {
		if err := os.MkdirAll(filepath.Join(basePath, filepath.FromSlash(emptyDirectory)), 0o755); err != nil {
			t.Fatalf("创建迁移遗留空目录失败: %v", err)
		}
	}

	application := &framework.App{BasePath: basePath, ApplicationPath: filepath.Join(basePath, "app")}
	if err := RefreshControllerDiscovery(application); err != nil {
		t.Fatalf("生成原生多应用装配入口失败: %v", err)
	}

	rootSource := readGeneratedDiscoverySource(t, basePath, "app/autoload_generated.go")
	for _, expected := range []string{
		"RegisterApplications(registerGlobalApplication",
		`applicationAdmin.Definition()`,
		`applicationIndex.Definition()`,
		`applicationAdmin "example.com/project/app/admin"`,
		`applicationIndex "example.com/project/app/index"`,
		"RegisterGlobalMiddleware",
	} {
		if !strings.Contains(rootSource, expected) {
			t.Errorf("根应用装配入口缺少 %q:\n%s", expected, rootSource)
		}
	}
	if strings.Contains(rootSource, "applicationController") || strings.Contains(rootSource, "internal") {
		t.Fatalf("根应用不得装配业务控制器或创建 internal 概念:\n%s", rootSource)
	}
	if strings.Contains(rootSource, "applicationEmpty") || strings.Contains(rootSource, `"example.com/project/app/view"`) {
		t.Fatalf("空目录不得被识别为业务应用:\n%s", rootSource)
	}

	indexSource := readGeneratedDiscoverySource(t, basePath, "app/index/autoload_generated.go")
	for _, expected := range []string{
		"package index",
		`Name: "index"`,
		"Middleware: Middleware()",
		"applicationController.Index",
		"applicationModel.User",
		"applicationValidate.User",
		"applicationRoute.Load",
		"Events()",
		"Providers()",
		"Services:",
		"Services()",
	} {
		if !strings.Contains(indexSource, expected) {
			t.Errorf("index 应用装配入口缺少 %q:\n%s", expected, indexSource)
		}
	}

	adminSource := readGeneratedDiscoverySource(t, basePath, "app/admin/autoload_generated.go")
	for _, expected := range []string{"package admin", `Name: "admin"`, "applicationController.User", "applicationRoute.Load"} {
		if !strings.Contains(adminSource, expected) {
			t.Errorf("admin 应用装配入口缺少 %q:\n%s", expected, adminSource)
		}
	}
	for _, unexpected := range []string{"Events()", "Middleware()", "Providers()", "Services()"} {
		if strings.Contains(adminSource, unexpected) {
			t.Errorf("未声明可选入口时不得生成 %q 调用:\n%s", unexpected, adminSource)
		}
	}
	if err := CheckControllerDiscovery(application); err != nil {
		t.Fatalf("刚生成的多应用发现文件应通过校验: %v", err)
	}
}

func readGeneratedDiscoverySource(t *testing.T, basePath, relativePath string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(basePath, filepath.FromSlash(relativePath)))
	if err != nil {
		t.Fatalf("读取生成文件 %q 失败: %v", relativePath, err)
	}
	return string(content)
}
