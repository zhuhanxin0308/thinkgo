package framework

// ServiceProvider 服务提供者接口
// 对应 ThinkPHP 8 的 think\Service
// 将框架初始化逻辑拆分为独立的可插拔模块
type ServiceProvider interface {
	// Register 注册服务到容器（在所有 Provider 的 Boot 之前调用）
	// 用于绑定接口到实现、注册工厂函数等
	Register(app *App)

	// Boot 启动服务（在所有 Provider 的 Register 之后调用）
	// 用于执行需要其他服务已就绪的初始化逻辑
	Boot(app *App)
}
