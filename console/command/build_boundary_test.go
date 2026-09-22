package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// TestBuildConfiguredResourcesAndNonLinux 验证自定义目录和 ARM 参数，不给其他平台生成不可运行的 Docker 文件。
func TestBuildConfiguredResourcesAndNonLinux(t *testing.T) {
	base := t.TempDir()
	for path, content := range map[string]string{
		"config/app.json":               `{"public_path":"web","exception_tmpl":"errors/error.html"}`,
		"config/view.json":              `{"view_dir_name":"templates","view_path":"pages"}`,
		"app/admin/config/view.json":    `{"view_path":"admin-pages"}`,
		"app/admin/templates/page.html": "template", "app/admin/lang/en.json": `{}`,
		"web/assets/main.css": "css", "web/.well-known/security.txt": "contact", "web/.env": "secret",
		"pages/index.html": "page", "admin-pages/index.html": "admin", "errors/error.html": "error",
		"templates/admin/fallback.html": "fallback", "view/global.html": "shared",
	} {
		writeArtifactFixture(t, base, path, content)
	}
	target, _ := parseBuildTarget("windows/amd64", "")
	compile := func(_ context.Context, _, executable string, _ buildTarget) error {
		return os.WriteFile(executable, []byte("binary"), 0755)
	}
	if err := buildDistribution(context.Background(), base, target, compile); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"thinkgo-nocgo.exe", "web/assets/main.css", "web/.well-known/security.txt", "pages/index.html", "admin-pages/index.html", "errors/error.html", "app/admin/templates/page.html", "templates/admin/fallback.html", "view/global.html"} {
		if _, err := os.Stat(filepath.Join(base, "dist/windows-amd64", path)); err != nil {
			t.Errorf("自定义资源丢失: %s %v", path, err)
		}
	}
	for _, path := range []string{"Dockerfile", "docker-compose.yml", "web/.env"} {
		if _, err := os.Stat(filepath.Join(base, "dist/windows-amd64", path)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("不应包含 %s: %v", path, err)
		}
	}
}

// TestBuildRejectsUnsafeResourcesAndLocks 验证目录越界、配置损坏、并发构建和异常目标类型。
func TestBuildRejectsUnsafeResourcesAndLocks(t *testing.T) {
	for _, path := range []string{"../outside", ".", "dist", "runtime", "framework", "app", ".git", "cmd", "go.mod"} {
		if err := validateDistributionResourcePath(path); err == nil {
			t.Errorf("接受了危险资源目录 %q", path)
		}
	}
	for _, scenario := range []string{"config", "lock", "target-file", "missing-binary", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			base := t.TempDir()
			writeArtifactFixture(t, base, "config/app.json", `{}`)
			writeArtifactFixture(t, base, "app/index/lang/zh-cn.json", `{}`)
			switch scenario {
			case "config":
				writeArtifactFixture(t, base, "config/app.json", `{"public_path":12}`)
			case "lock":
				writeArtifactFixture(t, base, "dist/.build.lock", "locked")
			case "target-file":
				writeArtifactFixture(t, base, "dist/linux-amd64", "original")
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(base, "public")); err != nil {
					t.Skipf("本机不能创建链接: %v", err)
				}
			}
			target, _ := parseBuildTarget("linux/amd64", "")
			compile := func(_ context.Context, _, executable string, _ buildTarget) error {
				if scenario == "missing-binary" {
					return nil
				}
				return os.WriteFile(executable, []byte("binary"), 0755)
			}
			if err := buildDistribution(context.Background(), base, target, compile); err == nil {
				t.Fatal("危险构建没有被拒绝")
			}
		})
	}
}

// TestBuildAndCreateCommandErrors 验证公开入口在缺失依赖和非法参数时返回可诊断错误。
func TestBuildAndCreateCommandErrors(t *testing.T) {
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	app := framework.NewConsoleAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	for _, command := range []console.ICommand{&Build{Command: console.Command{App: app}}, &Create{Command: console.Command{App: app}}} {
		if err := command.Execute(nil, output); !errors.Is(err, console.ErrInvalidInput) {
			t.Fatal(err)
		}
		if err := command.Execute(console.NewInput(), nil); !errors.Is(err, console.ErrInvalidOutput) {
			t.Fatal(err)
		}
		if err := command.Execute(console.NewInput("../unsafe"), output); err == nil {
			t.Fatal("非法参数未被拒绝")
		}
	}
	for _, command := range []console.ICommand{&Build{}, &Create{}} {
		if err := command.Execute(console.NewInput(), output); !errors.Is(err, framework.ErrNilApplication) {
			t.Fatal(err)
		}
	}
	target, err := parseBuildTarget("", "")
	if err != nil || !strings.Contains(strings.Join(target.environment(), "\n"), "CGO_ENABLED=0") {
		t.Fatalf("默认环境错误: %v", err)
	}
}
