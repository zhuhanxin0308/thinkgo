package command

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// TestGeneratedProjectCommandBinaryRunsWithoutSources 验证实际项目模板的预编译入口在仅有制品与配置、无工具链和源码时仍可使用命令。
func TestGeneratedProjectCommandBinaryRunsWithoutSources(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("CGO_ENABLED", "0")
	for _, custom := range []bool{false, true} {
		name := "空静态清单"
		if custom {
			name = "自定义静态命令"
		}
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			sources, err := projectSources("example.com/staticcommands")
			if err != nil {
				t.Fatal(err)
			}
			for path, source := range sources {
				writeDiscoveryFixture(t, base, path, string(source))
			}
			writeStaticCommandModuleFixture(t, base)
			app := framework.NewConsoleAppUninitialized(base)
			t.Cleanup(func() { _ = app.Close() })
			if custom {
				generator := &MakeCommand{}
				generator.SetApp(app)
				if err := generator.Execute(console.NewInput("Audit", "audit:probe"), generatorTestOutput()); err != nil {
					t.Fatal(err)
				}
			} else if err := RefreshControllerDiscovery(app); err != nil {
				t.Fatal(err)
			}
			deployed := t.TempDir()
			for path, source := range sources {
				if strings.HasPrefix(filepath.ToSlash(path), "config/") || strings.HasPrefix(filepath.ToSlash(path), "public/") || strings.Contains(filepath.ToSlash(path), "/view/") {
					writeDiscoveryFixture(t, deployed, path, string(source))
				}
			}
			filename := "think"
			if runtime.GOOS == "windows" {
				filename += ".exe"
			}
			executable := filepath.Join(deployed, filename)
			build := exec.Command("go", "build", "-mod=readonly", "-tags=thinkgo_runtime", "-o", executable, "./cmd/think")
			build.Dir = base
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("构建实际项目入口失败: %v %s", err, output)
			}
			calls := [][]string{{"list"}, {"help"}, {"help", "list"}}
			if custom {
				calls = append(calls, []string{"help", "audit:probe"}, []string{"audit:probe"})
			}
			for _, args := range calls {
				process := exec.Command(executable, args...)
				process.Dir = deployed
				process.Env = append(os.Environ(), "PATH="+filepath.Join(deployed, "no-toolchain"), "GOROOT="+filepath.Join(deployed, "no-go"))
				output, err := process.CombinedOutput()
				if err != nil || len(output) == 0 {
					t.Fatalf("无源码/工具链执行 %v 失败: %v %s", args, err, output)
				}
				if custom && args[0] == "list" && !strings.Contains(string(output), "audit:probe") {
					t.Fatalf("静态命令未列出: %s", output)
				}
			}
		})
	}
}

func writeStaticCommandModuleFixture(t *testing.T, base string) {
	t.Helper()
	frameworkRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	module, err := os.ReadFile(filepath.Join(frameworkRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(string(module), "module "+frameworkImportPath, "module example.com/staticcommands", 1)
	content += "\nrequire " + frameworkImportPath + " v3.0.0\nreplace " + frameworkImportPath + " => " + filepath.ToSlash(frameworkRoot) + "\n"
	writeDiscoveryFixture(t, base, "go.mod", content)
	checksums, err := os.ReadFile(filepath.Join(frameworkRoot, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFixture(t, base, "go.sum", string(checksums))
}
