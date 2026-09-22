package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
)

// TestCreateProjectRequiresEmptyTarget 验证隐藏文件也会阻止创建，且已有内容保持原样。
func TestCreateProjectRequiresEmptyTarget(t *testing.T) {
	for _, existing := range []string{"", ".keep", "business.go"} {
		t.Run(existing, func(t *testing.T) {
			base := t.TempDir()
			if existing != "" {
				writeArtifactFixture(t, base, "demo/"+existing, "original")
			} else if err := os.Mkdir(filepath.Join(base, "demo"), 0755); err != nil {
				t.Fatal(err)
			}
			prepare := func(ctx context.Context, path, module string) error {
				return os.WriteFile(filepath.Join(path, "go.mod"), []byte("module "+module+"\n"), 0644)
			}
			err := createProject(context.Background(), base, "demo", prepare)
			if existing != "" {
				if err == nil {
					t.Fatal("非空目录不能创建项目")
				}
				content, readErr := os.ReadFile(filepath.Join(base, "demo", existing))
				if readErr != nil || string(content) != "original" {
					t.Fatalf("已有文件被改写: %s %v", content, readErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestCreatedProjectBuildsAndHandlesRequests 用当前框架真实编译生成工程并通过内存 HTTP 内核请求首页。
func TestCreatedProjectBuildsAndHandlesRequests(t *testing.T) {
	frameworkRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	prepare := func(ctx context.Context, directory, module string) error {
		content := fmt.Sprintf("module %s\n\ngo %s\n\nrequire %s v0.0.0\nreplace %s => %q\n", module, scaffoldGoVersion, scaffoldFrameworkModule, scaffoldFrameworkModule, filepath.ToSlash(frameworkRoot))
		if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte(content), 0644); err != nil {
			return err
		}
		// 使用仓库已经校验的依赖校验和，使本地框架集成测试不依赖公网校验服务。
		checksums, err := os.ReadFile(filepath.Join(frameworkRoot, "go.sum"))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(directory, "go.sum"), checksums, 0644); err != nil {
			return err
		}
		dependencies := exec.CommandContext(ctx, "go", "get", scaffoldFrameworkModule)
		dependencies.Dir = directory
		dependencies.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
		if output, err := dependencies.CombinedOutput(); err != nil {
			return fmt.Errorf("本地框架依赖获取失败: %w\n%s", err, output)
		}
		application := framework.NewConsoleAppUninitialized(directory)
		defer application.Close()
		if err := RefreshControllerDiscovery(application); err != nil {
			return err
		}
		process := exec.CommandContext(ctx, "go", "mod", "tidy")
		process.Dir = directory
		process.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
		if output, err := process.CombinedOutput(); err != nil {
			return fmt.Errorf("依赖解析失败: %w\n%s", err, output)
		}
		return nil
	}
	if err := createProject(context.Background(), base, "my-project", prepare); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"test", "-mod=readonly", "./..."}, {"run", "-mod=readonly", "./cmd/think", "list"}, {"run", "-mod=readonly", "./cmd/think", "route:list"}} {
		process := exec.Command("go", args...)
		process.Dir = filepath.Join(base, "my-project")
		process.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
		if output, err := process.CombinedOutput(); err != nil {
			t.Fatalf("生成项目 %v 失败: %v\n%s", args, err, output)
		}
	}
}

// TestCreateProjectRollbackAndNames 验证准备失败不会留下半个项目，也不会创建到当前目录之外。
func TestCreateProjectRollbackAndNames(t *testing.T) {
	base := t.TempDir()
	want := errors.New("依赖下载失败")
	prepare := func(context.Context, string, string) error { return want }
	if err := createProject(context.Background(), base, "demo", prepare); !errors.Is(err, want) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) != 0 {
		t.Fatalf("失败创建留下目录: %v %v", entries, err)
	}
	for _, name := range []string{"", ".", "..", "../demo", "a/b", `a\b`, "demo ", "CON", "nul", "demo.", "a:b"} {
		if err := createProject(context.Background(), base, name, prepare); err == nil || errors.Is(err, want) {
			t.Errorf("非法项目名未在写入前拒绝: %q %v", name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := createProject(ctx, base, "cancelled", prepare); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// TestProjectSourcesContainWorkingApplication 验证完整项目包含主入口、工具入口、配置、测试和多应用骨架。
func TestProjectSourcesContainWorkingApplication(t *testing.T) {
	sources, err := projectSources("my-project")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"main.go", "main_test.go", "cmd/think/main.go", "cmd/think/runtime.go", "app/provider.go", "app/request.go", "app/exception_handle.go", "app/service.go", "app/event.go", "app/middleware.go", "app/index/controller/index.go", "app/index/route/app.go", "app/index/view/index.html", "config/app.json", "config/database.json", "config/view.json", "app/lang/zh-cn.json", "public/robots.txt", ".gitignore"} {
		if len(sources[filepath.FromSlash(path)]) == 0 {
			t.Errorf("缺少项目文件: %s", path)
		}
	}
}
