package launcher

import (
	"context"
	"errors"
	"fmt"
	"go/format"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
	"github.com/zhuhanxin0308/thinkgo/v3/console/command"
)

// ErrProjectCommandRequiresBusiness 表示命令与参数已校验，需要由拥有进程生命周期的宿主接续业务装配。
var ErrProjectCommandRequiresBusiness = errors.New("项目命令需要业务装配宿主")

// ProjectCommandBusinessExitCode 是临时命令宿主向父进程请求接续业务执行的内部退出码。
const ProjectCommandBusinessExitCode = 78

func projectCommandBasePath(basePath string) string {
	if basePath == "" {
		return "."
	}
	return basePath
}

func projectCommandConsole(app *framework.App, stdout, stderr io.Writer, commands []console.ICommand) (*console.Console, error) {
	cli := console.NewConsole(app)
	if err := cli.SetOutput(console.NewOutputWithAutoColor(stdout, stderr)); err != nil {
		return nil, err
	}
	if err := RegisterCommands(cli); err != nil {
		return nil, err
	}
	for _, current := range commands {
		if err := cli.Register(current); err != nil {
			return nil, fmt.Errorf("注册项目命令失败: %w", err)
		}
	}
	return cli, nil
}

func isProjectCommandInformation(args []string) bool {
	return len(args) == 0 || args[0] == "list" || args[0] == "help"
}

func needsProjectCommands(args []string, builtins *console.Console) bool {
	if len(args) == 0 || args[0] == "list" || args[0] == "help" && len(args) == 1 {
		return true
	}
	name := args[0]
	if name == "help" {
		name = args[1]
	}
	return errors.Is(builtins.ShowCommandHelp(name, true), console.ErrCommandNotFound)
}

func isBuiltinCommand(name string) bool {
	cli, err := projectCommandConsole(nil, io.Discard, io.Discard, nil)
	return err == nil && cli.ShowCommandHelp(name, true) == nil
}

// projectCommandForExecution 先完成命令匹配与参数校验，再允许任何业务装配或数据库访问。
func projectCommandForExecution(ctx context.Context, args []string, commands []console.ICommand) (console.ICommand, error) {
	if isProjectCommandInformation(args) {
		return nil, nil
	}
	for _, current := range commands {
		if current.GetSignature() != args[0] {
			continue
		}
		input := console.NewInputContext(ctx, args[1:]...)
		if err := input.Parse(current.GetArgumentDefinitions(), current.GetOptionDefinitions()); err != nil {
			return nil, fmt.Errorf("命令 %q: %w", args[0], err)
		}
		return current, nil
	}
	return nil, nil
}

// runProjectCommandHost 首轮只编译命令包，真实 Configure 决定签名与帮助。
// 只有已匹配且参数有效的业务命令才进入第二轮含项目装配的宿主。
func runProjectCommandHost(ctx context.Context, basePath string, args []string, stdout, stderr io.Writer, commands []command.ProjectCommandType, business bool) (returnErr error) {
	base, err := filepath.Abs(projectCommandBasePath(basePath))
	if err != nil {
		return err
	}
	modulePath := ""
	if business {
		// #nosec G204 -- 模块查询参数固定，仅由工作目录选择当前项目，不经过 shell。
		process := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-m", "-f", "{{.Path}}")
		process.Dir = base
		process.Env = append(os.Environ(), "GOWORK=off")
		output, err := process.Output()
		if err != nil {
			return errors.Join(fmt.Errorf("读取项目模块失败: %w", err), ctx.Err())
		}
		modulePath = strings.TrimSpace(string(output))
	}
	source, err := projectCommandHostSource(commands, modulePath)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "thinkgo-commands-*")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, RemoveRuntimeDirectory(directory)) }()
	sourcePath := filepath.Join(directory, "main.go")
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		return err
	}
	filename := "think-commands"
	if runtime.GOOS == "windows" {
		filename += ".exe"
	}
	executable := filepath.Join(directory, filename)
	// #nosec G204 -- 编译参数固定，源文件和输出文件均在本次独占临时目录中，不经过 shell。
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", executable, sourcePath)
	if business {
		build.Args = append(build.Args[:len(build.Args)-1], "-tags="+runtimeBuildTag, sourcePath)
	}
	build.Dir, build.Stdout, build.Stderr = base, stdout, stderr
	build.Env = append(os.Environ(), "GOWORK=off")
	if err := build.Run(); err != nil {
		return errors.Join(fmt.Errorf("编译项目命令宿主失败: %w", err), ctx.Err())
	}
	// #nosec G204 G702 -- 只运行本次成功构建的程序，命令参数通过 argv 原样传递。
	process := exec.CommandContext(ctx, executable, args...)
	process.Dir, process.Stdin, process.Stdout, process.Stderr = base, os.Stdin, stdout, stderr
	err = process.Run()
	var exitError *exec.ExitError
	if !business && ctx.Err() == nil && errors.As(err, &exitError) && exitError.ExitCode() == ProjectCommandBusinessExitCode {
		// 探测进程已退出后，由同一个父进程直接管理业务宿主；取消不会留下业务孙进程。
		return runProjectCommandHost(ctx, base, args, stdout, stderr, commands, true)
	}
	return errors.Join(err, ctx.Err())
}

func projectCommandHostSource(commands []command.ProjectCommandType, modulePath string) ([]byte, error) {
	imports, values := command.ProjectCommandImportsAndValues(commands)
	registration := "nil"
	if modulePath != "" {
		imports += fmt.Sprintf("\tbusinessapp %q\n", modulePath+"/app")
		registration = "businessapp.Register"
	}
	source := fmt.Sprintf(`// 此临时宿主只装配本次命令执行需要的项目包。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
	"github.com/zhuhanxin0308/thinkgo/v3/console/launcher"
%s)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	commands := []console.ICommand{
%s	}
	err := launcher.RunWithCommands(ctx, "", os.Args[1:], os.Stdout, os.Stderr, %s, commands...)
	stop()
	if errors.Is(err, launcher.ErrProjectCommandRequiresBusiness) {
		os.Exit(launcher.ProjectCommandBusinessExitCode)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ThinkGo command failed: %%q\n", err.Error())
		os.Exit(1)
	}
}
`, imports, values, registration)
	formatted, err := format.Source([]byte(source))
	if err != nil {
		return nil, fmt.Errorf("生成项目命令宿主失败: %w", err)
	}
	return formatted, nil
}
