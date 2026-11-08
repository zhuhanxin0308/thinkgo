package main

import (
	adapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/echo"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/serve"
)

// main 仅链接Echo适配器，使用框架原生HTTP启动配置。
func main() {
	serve.Main(func(configuration business.Config, service *business.Service) (serve.Application, error) {
		return adapter.New(service, configuration.Address)
	})
}
