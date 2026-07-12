package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	_ "thinkgo/app/controller" // 注册控制器
	_ "thinkgo/app/middleware" // 注册全局中间件
	"thinkgo/framework"
	"thinkgo/framework/console"
	"thinkgo/framework/console/command"
	_ "thinkgo/route" // 注册路由
)

func main() {
	if err := runConsole(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		// 使用 %q 转义底层错误中的控制字符，避免终端注入和伪造多行日志。
		_, _ = fmt.Fprintf(os.Stderr, "ThinkGo command failed: %q\n", err.Error())
		os.Exit(1)
	}
}

// runConsole 装配并执行命令行应用，始终合并返回应用关闭错误。
func runConsole(args []string, stdout, stderr io.Writer) (returnErr error) {
	app := newConsoleApp(args)
	if app == nil {
		return framework.ErrNilApplication
	}
	defer func() {
		returnErr = errors.Join(returnErr, app.Close())
	}()

	cli := console.NewConsole(app)
	if err := cli.SetOutput(console.NewOutputWithAutoColor(stdout, stderr)); err != nil {
		return err
	}
	if err := registerDefaultCommands(cli); err != nil {
		return err
	}
	return cli.Run(args...)
}

// registerDefaultCommands 注册全部内置命令，任何定义冲突都会阻止 CLI 启动。
func registerDefaultCommands(cli *console.Console) error {
	if cli == nil {
		return fmt.Errorf("%w: Console 不能为空", console.ErrInvalidCommand)
	}
	commands := []console.ICommand{
		&command.Version{},
		&command.List{Console: cli},
		&command.Clear{},
		&command.Run{},
		&command.MakeController{},
		&command.MakeModel{},
		&command.MakeCommand{},
		&command.MakeValidate{},
		&command.MakeMiddleware{},
		&command.MakeEvent{},
		&command.MakeListener{},
		&command.MakeSubscribe{},
		&command.MakeService{},
		&command.RouteList{},
		&command.ConfigDump{},
	}
	for _, current := range commands {
		if err := cli.Register(current); err != nil {
			return fmt.Errorf("注册内置命令失败: %w", err)
		}
	}
	return nil
}

// newConsoleApp 根据显式命令参数选择初始化方式，不读取全局 os.Args。
func newConsoleApp(args []string) *framework.App {
	if len(args) > 0 && args[0] == "run" {
		return framework.NewApp()
	}
	return framework.NewConsoleApp()
}
