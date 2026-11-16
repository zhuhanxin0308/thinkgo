package command

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/winfile"
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

// TestBuildRejectsUnsafeResourcesAndLocks 验证目录越界、配置损坏、旧锁和异常目标类型。
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

// TestBuildLegacyLockRequiresMigration 验证旧锁始终保留，并提供一次性迁移提示。
func TestBuildLegacyLockRequiresMigration(t *testing.T) {
	base := t.TempDir()
	writeArtifactFixture(t, base, "dist/.build.lock", "previous lock")
	target, err := parseBuildTarget("windows/amd64", "")
	if err != nil {
		t.Fatal(err)
	}
	err = buildDistribution(t.Context(), base, target, func(context.Context, string, string, buildTarget) error {
		t.Error("检测到旧锁时不能开始编译")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "停止旧版构建") || !strings.Contains(err.Error(), "dist/.build.lock") {
		t.Fatalf("应提示停止旧版构建并清理旧锁: %v", err)
	}
	value, readErr := os.ReadFile(filepath.Join(base, "dist", ".build.lock"))
	if readErr != nil || string(value) != "previous lock" {
		t.Fatalf("旧锁必须保留以便人工确认: %q %v", value, readErr)
	}
}

// TestBuildMarkerSupportsFilesystemsWithoutLinks 验证原子目录标记可阻挡旧版并在退出后恢复。
func TestBuildMarkerSupportsFilesystemsWithoutLinks(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.Mkdir("dist", 0o700); err != nil {
		t.Fatal(err)
	}
	noLinks := func(string, string) error { return errors.ErrUnsupported }
	check := func() error {
		info, err := root.Lstat(distributionLegacyLockPath)
		if err != nil || !info.IsDir() {
			t.Fatalf("不支持硬链接时应持有目录标记: %v", err)
		}
		if old, err := root.OpenFile(distributionLegacyLockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
			_ = old.Close()
			t.Fatal("旧版不应取得已由目录标记占用的构建锁")
		}
		return nil
	}
	if err := withLegacyBuildMarker(root, noLinks, check); err != nil {
		t.Fatal(err)
	}
	// 强制退出会留下空目录标记，下一次持有内核锁的构建可直接接替。
	if err := root.Mkdir(distributionLegacyLockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := withLegacyBuildMarker(root, noLinks, check); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Lstat(distributionLegacyLockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("新版正常退出后应释放目录标记: %v", err)
	}
	if err := root.Mkdir(distributionLegacyLockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(distributionLegacyLockPath, "user-file")
	if err := root.WriteFile(nested, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := withLegacyBuildMarker(root, noLinks, check); !errors.Is(err, winfile.ErrUnsafeGuard) {
		t.Fatalf("占用的非空目录应明确阻断: %v", err)
	}
	if data, err := root.ReadFile(nested); err != nil || string(data) != "keep" {
		t.Fatalf("未知目录中的文件必须保留: %v", err)
	}
}

// TestBuildRecoversAfterLockOwnerIsKilled 通过两个真实进程验证构建互斥与崩溃恢复。
func TestBuildRecoversAfterLockOwnerIsKilled(t *testing.T) {
	const crashTestBase = "THINKGO_BUILD_CRASH_TEST_BASE"
	const readyMessage = "build-lock-acquired"
	target, err := parseBuildTarget("windows/amd64", "")
	if err != nil {
		t.Fatal(err)
	}
	if base := os.Getenv(crashTestBase); base != "" {
		if err := buildDistribution(t.Context(), base, target, func(context.Context, string, string, buildTarget) error {
			if _, err := fmt.Fprintln(os.Stdout, readyMessage); err != nil {
				return err
			}
			_, err := io.Copy(io.Discard, os.Stdin)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return
	}

	base := t.TempDir()
	writeArtifactFixture(t, base, "config/app.json", `{}`)
	writeArtifactFixture(t, base, "app/index/lang/zh-cn.json", `{}`)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBuildRecoversAfterLockOwnerIsKilled$")
	process.Env = append(os.Environ(), crashTestBase+"="+base)
	stdout, err := process.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := process.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if process.ProcessState == nil {
			_ = process.Process.Kill()
			_ = process.Wait()
		}
	}()
	reader := bufio.NewScanner(stdout)
	if !reader.Scan() || reader.Text() != readyMessage {
		t.Fatalf("构建子进程未进入编译: %q %v", reader.Text(), reader.Err())
	}
	legacyPath := filepath.Join(base, distributionLegacyLockPath)
	legacy, err := os.OpenFile(legacyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		_ = legacy.Close()
		t.Fatal("新版构建先启动时，旧版仍然取得了构建锁")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("旧版应明确遇到占用的构建锁: %v", err)
	}
	wait, stopWaiting := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stopWaiting()
	if err := buildDistribution(wait, base, target, func(context.Context, string, string, buildTarget) error {
		t.Error("构建锁仍在另一进程中，不应并发编译")
		return nil
	}); !errors.Is(err, winfile.ErrGuardInUse) {
		t.Errorf("已有构建占用锁时应立即返回: %v", err)
	}
	if err := process.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err == nil {
		t.Fatal("构建子进程应被强制终止")
	}
	err = buildDistribution(ctx, base, target, func(_ context.Context, _, executable string, _ buildTarget) error {
		return os.WriteFile(executable, []byte("binary"), 0o755)
	})
	if err != nil {
		t.Fatalf("进程被终止后应能立即重新构建: %v", err)
	}
	if _, err := os.Lstat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("正常退出的新版必须释放旧版兼容标记: %v", err)
	}
	path := filepath.Join(base, "dist", target.directory(), target.binary())
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "binary" {
		t.Fatalf("恢复后的构建产物无效: %q %v", data, err)
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
