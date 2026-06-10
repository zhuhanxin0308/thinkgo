package main

import (
	_ "thinkgo/app/controller" // 注册控制器
	_ "thinkgo/app/middleware" // 注册全局中间件
	"thinkgo/framework"
	"thinkgo/framework/http"
	_ "thinkgo/route" // 注册路由
)

func main() {
	// 创建应用实例
	app := framework.NewApp()

	// 创建HTTP内核并绑定到应用
	app.Kernel = http.NewHttp(app)

	// 启动应用
	app.Run()
}
