package main

import (
	adapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/fiber"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/serve"
)

// main 仅链接Fiber适配器，不启用prefork或额外工作进程。
func main() {
	serve.Main(func(configuration business.Config, service *business.Service) (serve.Application, error) {
		return adapter.New(service, configuration.Address)
	})
}
