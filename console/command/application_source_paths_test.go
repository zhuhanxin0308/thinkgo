package command

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDecodeApplicationSourcePackagesAcceptsPathAliases 验证模块根和源码使用不同路径别名时仍属于同一模块。
func TestDecodeApplicationSourcePackagesAcceptsPathAliases(t *testing.T) {
	base := t.TempDir()
	directory := filepath.Join("app", "index", "controller")
	writeDiscoveryFixture(t, base, filepath.Join(directory, "user.go"), "package controller\n\ntype User struct{}\n")
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	alias := createApplicationModuleAlias(t, realBase)
	secondAlias := createApplicationModuleAlias(t, realBase)
	for _, scenario := range []struct {
		name      string
		base      string
		source    string
		directory string
	}{
		{name: "真实路径", base: realBase, source: realBase, directory: directory},
		{name: "模块根别名", base: alias, source: realBase, directory: directory},
		{name: "源码路径别名", base: realBase, source: alias, directory: directory},
		{name: "两种不同别名", base: alias, source: secondAlias, directory: directory},
		{name: "模块根包", base: alias, source: realBase, directory: "."},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			output := encodeApplicationSourcePackage(t, filepath.Join(scenario.source, scenario.directory))
			packages, err := decodeApplicationSourcePackages(scenario.base, output)
			if err != nil {
				t.Fatalf("同一模块的路径别名被拒绝: %v", err)
			}
			if pkg, exists := packages[scenario.directory]; len(packages) != 1 || !exists || pkg.Name != "controller" {
				t.Fatalf("源码未按模块内相对路径归档: %#v", packages)
			}
		})
	}
}

// TestDecodeApplicationSourcePackagesRejectsOutsideModule 验证真实越界目录和借助模块内链接的越界都被拒绝。
func TestDecodeApplicationSourcePackagesRejectsOutsideModule(t *testing.T) {
	parent := t.TempDir()
	base := filepath.Join(parent, "module")
	outside := filepath.Join(parent, "module-other")
	for _, directory := range []string{base, outside} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("模块外真实目录", func(t *testing.T) {
		_, err := decodeApplicationSourcePackages(base, encodeApplicationSourcePackage(t, outside))
		if err == nil || !strings.Contains(err.Error(), "不属于当前模块") {
			t.Fatalf("同名前缀的模块外目录应被拒绝: %v", err)
		}
	})
	t.Run("模块内链接逃逸", func(t *testing.T) {
		link := filepath.Join(base, "escape")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("当前环境无法创建目录符号链接: %v", err)
		}
		_, err := decodeApplicationSourcePackages(base, encodeApplicationSourcePackage(t, link))
		if err == nil || !strings.Contains(err.Error(), "不属于当前模块") {
			t.Fatalf("模块内链接不能让外部源码进入模块: %v", err)
		}
	})
}

// TestDecodeApplicationSourcePackagesRejectsUnavailableDirectories 验证扫描期间消失的模块或源码目录不能被当作有效包。
func TestDecodeApplicationSourcePackagesRejectsUnavailableDirectories(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "missing")
	for _, scenario := range []struct {
		name   string
		base   string
		source string
	}{
		{name: "模块根已消失", base: missing, source: missing},
		{name: "源码目录已消失", base: base, source: missing},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, err := decodeApplicationSourcePackages(scenario.base, encodeApplicationSourcePackage(t, scenario.source))
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("不存在的目录必须保留可检查的错误: %v", err)
			}
		})
	}
}

// TestDiscoveryAndOpenAPIThroughModuleAlias 验证真实 Go 源码枚举、类型发现和注释读取都支持模块根别名。
func TestDiscoveryAndOpenAPIThroughModuleAlias(t *testing.T) {
	base := t.TempDir()
	writeDiscoveryFixture(t, base, "go.mod", "module example.com/pathalias\n\ngo 1.26.6\n")
	writeDiscoveryFixture(t, base, "app/index/controller/user.go", "package controller\n\ntype User struct{}\n\n// Show 查询用户。\nfunc (*User) Show() string { return \"user\" }\n")
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	alias := createApplicationModuleAlias(t, realBase)
	// Unix 的 Go 子进程按真实工作目录返回源码路径，重现 macOS 临时目录的别名差异。
	t.Setenv("PWD", realBase)
	t.Run("应用类型发现", func(t *testing.T) {
		names, found, err := discoverApplicationStructTypes(alias, "app/index/controller", "controller")
		if err != nil || !found || len(names) != 1 || names[0] != "User" {
			t.Fatalf("别名目录中的控制器发现失败: names=%v found=%v err=%v", names, found, err)
		}
	})
	t.Run("OpenAPI注释读取", func(t *testing.T) {
		source, err := scanOpenAPIComments(context.Background(), alias)
		if err != nil {
			t.Fatal(err)
		}
		if len(source.Files) != 1 || source.Files["app/index/controller/user.go"] == "" {
			t.Fatalf("源码文件必须保持模块内相对路径: %#v", source.Files)
		}
		if source.Handlers["example.com/pathalias/app/index/controller.User.Show"].Summary != "查询用户。" {
			t.Fatalf("别名目录中的处理器注释丢失: %#v", source.Handlers)
		}
	})
}

func createApplicationModuleAlias(t *testing.T, base string) string {
	t.Helper()
	alias := filepath.Join(t.TempDir(), "module-alias")
	if err := os.Symlink(base, alias); err != nil {
		t.Skipf("当前环境无法创建目录符号链接: %v", err)
	}
	return alias
}

func encodeApplicationSourcePackage(t *testing.T, directory string) []byte {
	t.Helper()
	output, err := json.Marshal(commentPackage{Dir: directory, Name: "controller", GoFiles: []string{"user.go"}})
	if err != nil {
		t.Fatal(err)
	}
	return output
}
