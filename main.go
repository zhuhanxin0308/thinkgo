package main

import (
	"fmt"
	"os"

	_ "thinkgo/app/controller" // 注册控制器
	_ "thinkgo/app/middleware" // 注册全局中间件
	"thinkgo/framework"
	"thinkgo/framework/http"
	_ "thinkgo/route" // 注册路由
)

func main() {
	// 创建应用实例
	app := framework.NewApp()

	// 创建 HTTP 内核并绑定到应用；配置错误必须在监听端口前失败。
	kernel, err := http.NewHttp(app)
	if err != nil {
		_ = app.Close()
		fmt.Fprintln(os.Stderr, "HTTP kernel initialization failed:", err)
		os.Exit(1)
	}
	app.Kernel = kernel

	// 启动应用
	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Application exited with error:", err)
		os.Exit(1)
	}
}
