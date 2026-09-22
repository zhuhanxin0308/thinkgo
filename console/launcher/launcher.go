package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
	"github.com/zhuhanxin0308/thinkgo/framework/console/command"
	"github.com/zhuhanxin0308/thinkgo/framework/db/connector"
	_ "time/tzdata" // 内置时区数据，保证 Windows CLI 制品可加载配置中的时区。
)

const (
	runtimeBuildTag              = "thinkgo_runtime"
	runtimeCleanupWindow         = 2 * time.Second
	RuntimeCleanupInitialBackoff = 5 * time.Millisecond
	runtimeCleanupMaxBackoff     = 50 * time.Millisecond
	// Windows 原生错误码用于识别短暂文件占用，不将永久的路径错误当作可重试失败。
	windowsSharingViolation syscall.Errno = 32
	windowsLockViolation    syscall.Errno = 33
)

// Run 为独立工具、项目入口及嵌入宿主提供一致的可取消命令生命周期。
func Run(ctx context.Context, basePath string, args []string, stdout, stderr io.Writer, register func(*framework.App) error) (returnErr error) {
	if ctx == nil {
		return fmt.Errorf("%w: 控制台执行上下文不能为空", console.ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if stdout == nil || stderr == nil {
		return console.ErrInvalidOutput
	}
	args = console.NormalizeInformationalArguments(args)
	if register == nil && NeedsBusiness(args) {
		return RunRuntime(ctx, basePath, args, stdout, stderr)
	}
	application, err := BuildApplication(basePath, args, register)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, application.Close())
	}()

	cli := console.NewConsole(application)
	if err := cli.SetOutput(console.NewOutputWithAutoColor(stdout, stderr)); err != nil {
		return err
	}
	if err := RegisterCommands(cli); err != nil {
		return err
	}
	return cli.RunContext(ctx, args...)
}

// BuildApplication 构造并初始化与 HTTP 入口相同的项目 App。
func BuildApplication(basePath string, args []string, register func(*framework.App) error) (*framework.App, error) {
	skipDatabase := !NeedsDatabase(args)
	if !skipDatabase {
		connector.RegisterBuiltins()
	}
	var application *framework.App
	if skipDatabase {
		if basePath == "" {
			application = framework.NewConsoleAppUninitialized()
		} else {
			application = framework.NewConsoleAppUninitialized(basePath)
		}
	} else {
		if basePath == "" {
			application = framework.NewApp()
		} else {
			application = framework.NewApp(basePath)
		}
	}
	// 源码生成只需要项目根目录，避免依赖数据库、配置或业务 Provider 的启动副作用。
	if NeedsOnlySource(args) {
		return application, nil
	}
	if NeedsBusiness(args) {
		if register == nil {
			return nil, errors.Join(fmt.Errorf("业务命令需要 %s 构建标签", runtimeBuildTag), application.Close())
		}
		if err := register(application); err != nil {
			return nil, errors.Join(fmt.Errorf("应用装配失败: %w", err), application.Close())
		}
	}
	if err := application.Initialize(); err != nil {
		return nil, errors.Join(fmt.Errorf("应用初始化失败: %w", err), application.Close())
	}
	if err := application.BootProviders(); err != nil {
		return nil, errors.Join(fmt.Errorf("应用服务启动失败: %w", err), application.Close())
	}
	return application, nil
}

// NeedsOnlySource 保证源码工具和帮助不受业务配置或启动状态影响。
func NeedsOnlySource(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "build", "create", "service:discover", command.OpenAPIGenerateSignature, "version", "help", "list":
		return true
	default:
		return false
	}
}

// NeedsBusiness 只为执行编译期业务清单的命令装配业务包。
func NeedsBusiness(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "run", "migrate", "migrate:rollback", "migrate:status", "deploy:check", "route:list", "route:export", "schema:validate", "config:dump", "optimize", "optimize:config", "optimize:route", "optimize:schema", "clear":
		return true
	default:
		return false
	}
}

// RunRuntime 编译并运行项目宿主，取消时终止子进程并清理临时目录。
func RunRuntime(ctx context.Context, basePath string, args []string, stdout, stderr io.Writer) (returnErr error) {
	base, err := filepath.Abs(basePath)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "thinkgo-cli-*")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, RemoveRuntimeDirectory(directory)) }()
	filename := "think-runtime"
	if runtime.GOOS == "windows" {
		filename += ".exe"
	}
	executable := filepath.Join(directory, filename)
	// #nosec G204 -- 构建工具、包路径和构建参数固定，输出路径来自独占临时目录，不经过 shell。
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-tags="+runtimeBuildTag, "-o", executable, "./cmd/think")
	build.Dir, build.Stdout, build.Stderr = base, stdout, stderr
	if err := build.Run(); err != nil {
		return errors.Join(fmt.Errorf("编译业务命令宿主失败: %w", err), ctx.Err())
	}
	// #nosec G204 G702 -- 只启动本次构建的独占宿主，用户参数按 argv 原样传递，不拼接命令或执行 shell。
	process := exec.CommandContext(ctx, executable, args...)
	process.Dir, process.Stdin, process.Stdout, process.Stderr = base, os.Stdin, stdout, stderr
	return errors.Join(process.Run(), ctx.Err())
}

// RemoveRuntimeDirectory 等待 Windows 已退出进程的映像句柄释放，并保留最终清理错误。
func RemoveRuntimeDirectory(directory string) error {
	deadline := time.Now().Add(runtimeCleanupWindow)
	backoff := RuntimeCleanupInitialBackoff
	for {
		err := os.RemoveAll(directory)
		if err == nil || runtime.GOOS != "windows" || time.Now().After(deadline) {
			return err
		}
		if !errors.Is(err, os.ErrPermission) && !errors.Is(err, windowsSharingViolation) && !errors.Is(err, windowsLockViolation) {
			return err
		}
		time.Sleep(min(backoff, time.Until(deadline)))
		backoff = min(backoff*2, runtimeCleanupMaxBackoff)
	}
}

// NeedsDatabase 判断命令是否需要显式数据库初始化。
func NeedsDatabase(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "run", "migrate", "migrate:rollback", "migrate:status", "deploy:check", "optimize", "optimize:schema":
		return true
	case "schema:validate":
		definition := &command.SchemaValidate{}
		definition.Configure()
		input := console.NewInput(args[1:]...)
		if err := input.Parse(definition.GetArgumentDefinitions(), definition.GetOptionDefinitions()); err != nil {
			return false
		}
		return strings.TrimSpace(input.GetOption("table")) != ""
	default:
		return false
	}
}

// RegisterCommands 注册全部内置命令，任何定义冲突都会阻止 CLI 启动。
func RegisterCommands(cli *console.Console) error {
	if cli == nil {
		return fmt.Errorf("%w: Console 不能为空", console.ErrInvalidCommand)
	}
	commands := []console.ICommand{
		&command.Version{},
		&command.Help{Console: cli},
		&command.List{Console: cli},
		&command.Build{},
		&command.Create{},
		&command.MakeApp{},
		&command.Clear{},
		&command.Run{},
		&command.MakeController{},
		&command.MakeModel{},
		&command.MakeCRUD{},
		&command.OpenAPIGenerate{},
		&command.MakeCommand{},
		&command.MakeValidate{},
		&command.MakeMiddleware{},
		&command.MakeEvent{},
		&command.MakeListener{},
		&command.MakeSubscribe{},
		&command.MakeService{},
		&command.Optimize{},
		&command.OptimizeConfig{},
		&command.OptimizeRoute{},
		&command.OptimizeSchema{},
		&command.RouteExport{},
		&command.SchemaValidate{},
		&command.RouteList{},
		&command.ServiceDiscover{},
		&command.VendorPublish{},
		&command.ConfigDump{},
		&command.Migrate{},
		&command.MigrateRollback{},
		&command.MigrateStatus{},
		&command.DeployCheck{},
	}
	for _, current := range commands {
		if err := cli.Register(current); err != nil {
			return fmt.Errorf("注册内置命令失败: %w", err)
		}
	}
	return nil
}
