package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	"golang.org/x/mod/modfile"
)

// TestCrossBuildPreservesHostDiscovery 使用真实宿主与目标平台类型执行完整生成和编译链。
func TestCrossBuildPreservesHostDiscovery(t *testing.T) {
	base := t.TempDir()
	sources, err := projectSources("example.com/buildisolation")
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range sources {
		writeDiscoveryFixture(t, base, path, string(content))
	}
	writeDiscoveryModuleFixture(t, base, "example.com/buildisolation")
	// 复用已锁定的完整依赖图，构建验收不额外下载依赖自身的测试依赖。
	frameworkRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	moduleBytes, err := os.ReadFile(filepath.Join(frameworkRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	module, err := modfile.Parse("go.mod", moduleBytes, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		module.AddModuleStmt("example.com/buildisolation"),
		module.AddRequire("github.com/zhuhanxin0308/thinkgo/v3", "v3.0.0"),
		module.AddReplace("github.com/zhuhanxin0308/thinkgo/v3", "", filepath.ToSlash(frameworkRoot), ""),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	moduleBytes, err = module.Format()
	if err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFixture(t, base, "go.mod", string(moduleBytes))
	targetOS := "windows"
	if runtime.GOOS == targetOS {
		targetOS = "linux"
	}
	for platform, name := range map[string]string{runtime.GOOS: "HostOnly", targetOS: "TargetOnly"} {
		writeDiscoveryFixture(t, base, "app/index/controller/platform_"+platform+".go",
			"//go:build "+platform+"\n\npackage controller\n\ntype "+name+" struct { BaseController }\nfunc (*"+name+") Index() string { return \"fixture\" }\n")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	host := buildTarget{os: runtime.GOOS, arch: runtime.GOARCH}
	var logs bytes.Buffer
	app := framework.NewAppUninitialized(base)
	t.Cleanup(func() { _ = app.Close() })
	if err := RefreshControllerDiscovery(app); err != nil {
		t.Fatal(err)
	}
	type snapshot struct {
		content []byte
		info    os.FileInfo
	}
	before := make(map[string]snapshot)
	for _, path := range []string{"autoload_generated.go", "cmd/think/autoload_generated.go", "app/autoload_generated.go", "app/index/autoload_generated.go", "go.mod", "go.sum"} {
		content, err := os.ReadFile(filepath.Join(base, path))
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(base, path))
		if err != nil {
			t.Fatal(err)
		}
		before[path] = snapshot{content, info}
	}
	target := buildTarget{os: targetOS, arch: runtime.GOARCH}
	for _, scenario := range []string{"cross", "host", "cross-again", "failure", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			currentTarget := target
			if scenario == "host" {
				currentTarget = host
			}
			if scenario == "failure" {
				path := "broken_" + targetOS + ".go"
				writeDiscoveryFixture(t, base, path, "package main\nvar _ = missingBuildFixture\n")
				t.Cleanup(func() { _ = os.Remove(filepath.Join(base, path)) })
			}
			child, cancelBuild := context.WithCancel(ctx)
			defer cancelBuild()
			var overlayPath string
			runner := func(ctx context.Context, base string, env []string, output io.Writer, command string, args ...string) error {
				err := runBuildProcess(ctx, base, env, output, command, args...)
				if command != "go" && len(args) == 3 && args[0] == "service:discover" {
					overlayPath = args[2]
					if scenario == "cancel" && err == nil {
						cancelBuild()
					}
				}
				return err
			}
			output := filepath.Join(t.TempDir(), currentTarget.binary())
			err := compileDistributionWithRunner(child, base, output, currentTarget, generatorTestOutput(), runner)
			if scenario == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("生成后取消未传播: %v", err)
				}
			} else if scenario == "failure" {
				if err == nil {
					t.Fatal("目标平台编译错误未传播")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if overlayPath == "" {
				t.Fatal("测试未经过目标发现阶段")
			}
			if _, err := os.Stat(overlayPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("构建结束后遗留 overlay: %v", err)
			}
			for path, previous := range before {
				content, err := os.ReadFile(filepath.Join(base, path))
				info, statErr := os.Stat(filepath.Join(base, path))
				if err != nil || statErr != nil || !bytes.Equal(content, previous.content) || !os.SameFile(info, previous.info) {
					t.Fatalf("构建改变宿主文件内容或身份: %s %v %v", path, err, statErr)
				}
			}
		})
	}
	if err := CheckControllerDiscovery(app); err != nil {
		t.Fatalf("交叉构建使宿主发现失效: %v", err)
	}
	if err := runBuildProcess(ctx, base, host.environment(), &logs, "go", "build", "-mod=readonly", "-o", filepath.Join(t.TempDir(), host.binary()), "."); err != nil {
		t.Fatalf("交叉构建后宿主无法编译: %v\n%s", err, logs.String())
	}
}
