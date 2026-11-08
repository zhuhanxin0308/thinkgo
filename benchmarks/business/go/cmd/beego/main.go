package main

import (
	adapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/beego"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/serve"
)

// main 仅链接Beego适配器，保留原生路由和过滤器链。
func main() {
	serve.Main(func(configuration business.Config, service *business.Service) (serve.Application, error) {
		return adapter.New(service, configuration.Address)
	})
}
