package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/zhuhanxin0308/thinkgo/v3/console/launcher"
)

// main 提供可以通过 go install 安装的独立命令入口。
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	status := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if status != 0 {
		os.Exit(status)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	err := launcher.Run(ctx, "", args, stdout, stderr, nil)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ThinkGo command failed: %q\n", err.Error())
		return 1
	}
	return 0
}
