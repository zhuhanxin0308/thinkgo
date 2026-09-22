package command

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
)

// TestDiscoveryHonorsCustomBuildTagsInDownstream 验证自定义标签中的控制器真实进入独立宿主注册表。
func TestDiscoveryHonorsCustomBuildTagsInDownstream(t *testing.T) {
	base := prepareSemanticDiscoveryModule(t)
	writeDiscoveryFixture(t, base, "app/index/controller/base.go", "package controller\n\ntype Common struct{}\n")
	writeDiscoveryFixture(t, base, "app/index/controller/enterprise.go", "//go:build enterprise\n\npackage controller\n\ntype Enterprise struct{}\n")
	writeDiscoveryFixture(t, base, "app/index/controller/community.go", "//go:build !enterprise\n\npackage controller\n\ntype Community struct{}\n")
	t.Setenv("GOFLAGS", "-tags=enterprise")
	app := &framework.App{BasePath: base, ApplicationPath: filepath.Join(base, "app")}
	if err := RefreshControllerDiscovery(app); err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFixture(t, base, "app/index/registration_test.go", `package index

import (
	"testing"
	framework "github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/testkit"
)

// TestTaggedControllerRegistration 检查真实生成的装配函数，避免只校验源码字符串。
func TestTaggedControllerRegistration(t *testing.T) {
	host := testkit.New(t, testkit.Options{Register: func(app *framework.App) error { return Definition().Register(app) }})
	if _, err := host.App().Make("Enterprise"); err != nil { t.Fatalf("构建标签控制器没有注册: %v", err) }
	if _, err := host.App().Make("Community"); err == nil { t.Fatal("未参与构建的控制器被注册") }
}
`)
	process := exec.Command("go", "test", "-mod=mod", "-count=1", "./app/index")
	process.Dir = base
	process.Env = append(os.Environ(), "GOWORK=off")
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("带构建标签的下游装配失败: %v\n%s", err, output)
	}
}

// TestDiscoveryTargetsForeignPlatformWithoutExecutingIt 验证宿主生成器为另一平台选择源码并真实交叉编译。
func TestDiscoveryTargetsForeignPlatformWithoutExecutingIt(t *testing.T) {
	base := prepareSemanticDiscoveryModule(t)
	targetOS := "linux"
	if runtime.GOOS == targetOS {
		targetOS = "windows"
	}
	writeDiscoveryFixture(t, base, "app/index/controller/host.go", fmt.Sprintf("//go:build %s\n\npackage controller\n\ntype HostOnly struct{}\n", runtime.GOOS))
	writeDiscoveryFixture(t, base, "app/index/controller/target.go", fmt.Sprintf("//go:build %s\n\npackage controller\n\ntype TargetOnly struct{}\n", targetOS))
	t.Setenv("GOOS", targetOS)
	t.Setenv("GOARCH", "arm64")
	t.Setenv("CGO_ENABLED", "0")
	t.Setenv("GOFLAGS", "")
	app := &framework.App{BasePath: base, ApplicationPath: filepath.Join(base, "app")}
	if err := RefreshControllerDiscovery(app); err != nil {
		t.Fatal(err)
	}
	generated := readGeneratedDiscoverySource(t, base, "app/index/autoload_generated.go")
	if !strings.Contains(generated, "TargetOnly") || strings.Contains(generated, "HostOnly") {
		t.Errorf("宿主生成器选择了错误平台控制器: %s", generated)
	}
	process := exec.Command("go", "build", "-mod=mod", "./app/...")
	process.Dir = base
	process.Env = append(os.Environ(), "GOWORK=off")
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("下游目标平台装配无法编译: %v\n%s", err, output)
	}
}

// TestDiscoveryIncludesUnderscoreApplicationPackages 验证合法下划线应用不会被 Go 的通配目录规则遗漏。
func TestDiscoveryIncludesUnderscoreApplicationPackages(t *testing.T) {
	base := prepareSemanticDiscoveryModule(t)
	writeDiscoveryFixture(t, base, "app/_admin/controller/index.go", "package controller\n\ntype Index struct{}\n")
	names, found, err := discoverApplicationStructTypes(base, "app/_admin/controller", "controller")
	if err != nil || !found || len(names) != 1 || names[0] != "Index" {
		t.Fatalf("显式应用目录被通配规则遗漏: names=%v found=%v err=%v", names, found, err)
	}
}

// TestDiscoveryUsesTargetIntegerWidth 验证 32 位目标不会使用宿主整数宽度接受不可编译的类型。
func TestDiscoveryUsesTargetIntegerWidth(t *testing.T) {
	base := prepareSemanticDiscoveryModule(t)
	writeDiscoveryFixture(t, base, "app/index/controller/wide.go", "package controller\n\ntype Wide struct { Data [1 << 32]byte }\n")
	t.Setenv("GOOS", "linux")
	t.Setenv("GOARCH", "arm")
	t.Setenv("GOARM", "7")
	t.Setenv("CGO_ENABLED", "0")
	if _, _, err := discoverApplicationStructTypes(base, "app/index/controller", "controller"); err == nil {
		t.Fatal("32 位目标不能接受超出整数范围的数组长度")
	}
}
