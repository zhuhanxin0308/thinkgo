package framework

import (
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
)

// TestApplicationAssemblyUsesContainerServices 验证应用装配阶段使用容器中的服务实例。
func TestApplicationAssemblyUsesContainerServices(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app := NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })

	replacement := middleware.NewPipeline()
	app.Instance(string(ServiceMiddleware), replacement)
	if err := app.Initialize(); err != nil {
		t.Fatalf("初始化测试应用失败: %v", err)
	}

	if replacement.ResolveAlias("recovery") == nil {
		t.Fatal("应用装配应把 recovery 中间件注册到容器解析出的中间件管线")
	}
	if app.middleware != replacement {
		t.Fatal("显式替换后的中间件服务应与应用内部快照保持一致")
	}
}
