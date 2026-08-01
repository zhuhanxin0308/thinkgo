package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	_ "thinkgo/app/index" // 注册 index 应用
	"thinkgo/framework"
	"thinkgo/framework/console"
	"thinkgo/framework/console/command"
	_ "time/tzdata" // 内置时区数据，保证 Windows CLI 制品可加载配置中的时区。
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
	manager, err := newConsoleManager(args)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, manager.Close())
	}()

	cli := console.NewConsoleWithManager(manager)
	if err := cli.SetOutput(console.NewOutputWithAutoColor(stdout, stderr)); err != nil {
		return err
	}
	if err := registerDefaultCommands(cli); err != nil {
		return err
	}
	return cli.Run(args...)
}

// newConsoleManager 按命令类型创建多应用管理器，避免控制台命令共享全局 App 状态。
func newConsoleManager(args []string) (*framework.ApplicationManager, error) {
	skipDatabase := len(args) == 0 || args[0] != "run"
	return framework.NewApplicationManagerFromDefinitions("", framework.ApplicationDefinitions(), skipDatabase)
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
