package main

import (
	adapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/thinkgo"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/serve"
)

// main 仅链接ThinkGo适配器，其余框架不会进入本二进制。
func main() {
	serve.Main(func(configuration business.Config, service *business.Service) (serve.Application, error) {
		return adapter.New(service, configuration.Address)
	})
}
