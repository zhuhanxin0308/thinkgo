package main

import (
	"os"

	_ "thinkgo/app/controller" // 注册控制器
	_ "thinkgo/app/middleware" // 注册全局中间件
	"thinkgo/framework"
	"thinkgo/framework/console"
	"thinkgo/framework/console/command"
	_ "thinkgo/route" // 注册路由
)

func main() {
	// 创建应用实例。
	// 仅 run 命令需要数据库；其余命令（version/list/make:* 等）跳过连库，
	// 避免每次执行都尝试连接数据库并打印连接失败日志。
	app := newConsoleApp()

	// 创建命令行应用
	cli := console.NewConsole(app)

	// 注册默认命令
	cli.Register(&command.Version{})
	cli.Register(&command.List{Console: cli})
	cli.Register(&command.Clear{})
	cli.Register(&command.Run{})
	cli.Register(&command.MakeController{})
	cli.Register(&command.MakeModel{})
	cli.Register(&command.MakeCommand{})
	cli.Register(&command.MakeValidate{})
	cli.Register(&command.MakeMiddleware{})
	cli.Register(&command.MakeEvent{})
	cli.Register(&command.MakeListener{})
	cli.Register(&command.MakeSubscribe{})
	cli.Register(&command.MakeService{})
	cli.Register(&command.RouteList{})
	cli.Register(&command.ConfigDump{})

	// 执行命令行调度
	cli.Run()
}

// newConsoleApp 根据将要执行的命令选择应用初始化方式：
// run 命令需要数据库，使用完整初始化；其余命令跳过数据库连接。
func newConsoleApp() *framework.App {
	if len(os.Args) > 1 && os.Args[1] == "run" {
		return framework.NewApp()
	}
	return framework.NewConsoleApp()
}
