package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
)

// TestDiscoveryParsesGoModuleSyntax 验证正常清单、源码 CLI 清单使用相同的 Go 模块解析。
func TestDiscoveryParsesGoModuleSyntax(t *testing.T) {
	const path = "example.com/project"
	base := t.TempDir()
	writeDiscoveryModuleFixture(t, base, path)
	original, err := os.ReadFile(filepath.Join(base, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFixture(t, base, "app/index/controller/index.go", "package controller\ntype Index struct{}\n")
	for _, directive := range []string{"module " + path, "module \"" + path + "\"", "module " + path + " // 项目模块"} {
		t.Run(directive, func(t *testing.T) {
			source := strings.Replace(string(original), "module "+path, directive, 1)
			writeDiscoveryFixture(t, base, "go.mod", source)
			app := &framework.App{BasePath: base}
			if err := RefreshControllerDiscovery(app); err != nil {
				t.Fatal(err)
			}
			got, err := ProjectApplicationAssemblySource(t.Context(), base)
			if err != nil || string(got) != readGeneratedDiscoverySource(t, base, applicationDiscoveryFilename) {
				t.Fatalf("源码 CLI 与 HTTP 清单不一致: %v\n%s", err, got)
			}
		})
	}
}

// TestDiscoveryRejectsInvalidModuleBeforeWriting 验证语法损坏和路径非法会阻断清单发布。
func TestDiscoveryRejectsInvalidModuleBeforeWriting(t *testing.T) {
	for _, source := range []string{"go 1.27.4\n", "module \"\"\n", "module example.com/../bad\n", "module \"broken\n", "module example.com/a\nmodule example.com/b\n"} {
		t.Run(source, func(t *testing.T) {
			base := t.TempDir()
			writeDiscoveryFixture(t, base, "go.mod", source)
			if _, err := readApplicationModulePath(base); err == nil {
				t.Fatal("无效模块声明应报错")
			}
		})
	}
}

// TestDiscoveryDropsApplicationsWithoutSelectedSources 验证旧清单不会使已移除的应用继续被注册。
func TestDiscoveryDropsApplicationsWithoutSelectedSources(t *testing.T) {
	base := t.TempDir()
	writeDiscoveryModuleFixture(t, base, "example.com/project")
	writeDiscoveryFixture(t, base, "app/index/controller/index.go", "package controller\ntype Index struct{}\n")
	writeDiscoveryFixture(t, base, "app/retired/controller/index.go", "package controller\ntype Index struct{}\n")
	writeDiscoveryFixture(t, base, "app/tagged/controller/sub/index.go", "//go:build discovery_fixture\n\npackage sub\ntype Index struct{}\n")
	writeDiscoveryFixture(t, base, "app/tagged/controller/sub/helper_test.go", "package sub\n")
	t.Setenv("GOFLAGS", "-tags=discovery_fixture")
	app := &framework.App{BasePath: base}
	if err := RefreshControllerDiscovery(app); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"retired", "tagged"} {
		if !strings.Contains(readGeneratedDiscoverySource(t, base, applicationDiscoveryFilename), "/app/"+name) {
			t.Fatalf("初次发现没有注册 %s", name)
		}
	}
	if err := os.Remove(filepath.Join(base, "app", "retired", "controller", "index.go")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", "")
	if err := RefreshControllerDiscovery(app); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{applicationDiscoveryFilename, "cmd/think/" + applicationDiscoveryFilename} {
		text := readGeneratedDiscoverySource(t, base, path)
		if strings.Contains(text, "/app/retired") || strings.Contains(text, "/app/tagged") || !strings.Contains(text, "/app/index") {
			t.Fatalf("入口清单未按有效业务源码更新: %s\n%s", path, text)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "app", "retired", applicationDiscoveryFilename)); err != nil {
		t.Fatalf("发现不应删除旧清单: %v", err)
	}
	if err := CheckControllerDiscovery(app); err != nil {
		t.Fatal(err)
	}
}
