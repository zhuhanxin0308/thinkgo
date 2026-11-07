package main

import (
	adapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/gin"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/serve"
)

// main 仅链接Gin适配器，连接池由共同生命周期负责关闭。
func main() {
	serve.Main(func(configuration business.Config, service *business.Service) (serve.Application, error) {
		return adapter.New(service, configuration.Address)
	})
}
