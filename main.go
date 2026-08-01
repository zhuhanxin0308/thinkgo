package main

import (
	"fmt"
	"os"

	_ "thinkgo/app/index"
	"thinkgo/framework"
	fwhttp "thinkgo/framework/http"
	_ "time/tzdata" // 内置时区数据，保证 Windows 发布制品可加载配置中的时区。
)

func main() {
	manager, err := framework.NewApplicationManager(resolveRuntimeBasePath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "应用管理器初始化失败:", err)
		os.Exit(1)
	}
	host, err := fwhttp.NewMultiHttp(manager)
	if err != nil {
		_ = manager.Close()
		fmt.Fprintln(os.Stderr, "统一 HTTP 宿主初始化失败:", err)
		os.Exit(1)
	}
	if err := host.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "应用退出并返回错误:", err)
		os.Exit(1)
	}
}
