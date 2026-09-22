package launcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// TestLauncherToolsWorkOutsideProjects 验证独立工具在无项目及配置损坏目录中仍能展示帮助、版本与源码命令。
func TestLauncherToolsWorkOutsideProjects(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "config"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "config", "app.json"), []byte("invalid"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--version"}, {"--help"}, {"build", "--help"}, {"create", "-h"}, {"list", "make", "--raw"}} {
		var output bytes.Buffer
		if err := Run(context.Background(), base, args, &output, &output, nil); err != nil || output.Len() == 0 {
			t.Fatalf("独立工具 %v 失败: %v %s", args, err, output.String())
		}
	}
	for _, args := range [][]string{{"build", "bad"}, {"create", "../bad"}, {"list", "missing"}, {"help", "missing"}, {"make:model", "User"}} {
		if err := Run(context.Background(), base, args, io.Discard, io.Discard, nil); err == nil {
			t.Fatalf("损坏输入 %v 未失败", args)
		}
	}
}

// TestLauncherLifecycleFailures 验证取消、业务装配错误、数据库边界和重复注册都会完整传播。
func TestLauncherLifecycleFailures(t *testing.T) {
	base := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, base, nil, io.Discard, io.Discard, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := Run(nil, base, nil, io.Discard, io.Discard, nil); !errors.Is(err, console.ErrInvalidInput) {
		t.Fatal(err)
	}
	if err := Run(context.Background(), base, nil, nil, io.Discard, nil); !errors.Is(err, console.ErrInvalidOutput) {
		t.Fatal(err)
	}
	if _, err := BuildApplication(base, []string{"route:list"}, nil); err == nil {
		t.Fatal("业务装配缺失未失败")
	}
	want := errors.New("装配失败")
	if _, err := BuildApplication(base, []string{"route:list"}, func(*framework.App) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	for _, name := range []string{"run", "migrate", "deploy:check", "optimize", "optimize:schema"} {
		if !NeedsDatabase([]string{name}) || !NeedsBusiness([]string{name}) {
			t.Errorf("%s 需要数据库及业务装配", name)
		}
	}
	for _, args := range [][]string{nil, {"help"}, {"schema:validate"}, {"schema:validate", "--unknown"}} {
		if NeedsDatabase(args) {
			t.Errorf("%v 不应连接数据库", args)
		}
	}
	if !NeedsDatabase([]string{"schema:validate", "--table", "users"}) || NeedsBusiness(nil) || !NeedsOnlySource(nil) {
		t.Fatal("源码和业务命令判断错误")
	}
	if err := RegisterCommands(nil); err == nil {
		t.Fatal("缺失控制台未失败")
	}
	cli := console.NewConsole(nil)
	if err := RegisterCommands(cli); err != nil {
		t.Fatal(err)
	}
	if err := RegisterCommands(cli); !errors.Is(err, console.ErrDuplicateCommand) {
		t.Fatal(err)
	}
}

// TestRuntimeLauncherCompilesAndCleans 验证运行时编译使用业务标签并正确转发参数，运行结束清理临时目录。
func TestRuntimeLauncherCompilesAndCleans(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "cmd", "think"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":            "module example.com/launcher\n\ngo 1.26.6\n",
		"cmd/think/main.go": "//go:build thinkgo_runtime\n\npackage main\nimport (\"fmt\"; \"os\")\nfunc main() { fmt.Println(os.Args[1]); fmt.Println(os.Executable()) }\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(base, path), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := Run(context.Background(), base, []string{"route:list"}, &output, &output, nil); err != nil {
		t.Fatalf("运行时编译失败: %v %s", err, output.String())
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || strings.TrimSpace(lines[0]) != "route:list" {
		t.Fatal(output.String())
	}
	executable := strings.TrimSuffix(strings.TrimSpace(lines[1]), " <nil>")
	if _, err := os.Stat(executable); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("临时二进制未清理: %v", err)
	}
	if err := RunRuntime(context.Background(), t.TempDir(), nil, io.Discard, io.Discard); err == nil {
		t.Fatal("缺失源码未失败")
	}
}
