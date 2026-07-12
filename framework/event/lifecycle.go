package event

// ==================== 框架生命周期事件 ====================
// 对应 ThinkPHP 的内置事件：AppInit/HttpRun/HttpEnd/RouteLoaded

// 以下常量定义框架内置的生命周期事件名称。
// 应用层可通过 Dispatcher.Listen() 注册对应事件的监听器。
const (
	// EventAppInit 应用初始化完成后触发。
	// 对应 ThinkPHP 的 AppInit 事件，在配置加载、服务注册完成后分发。
	EventAppInit = "framework.AppInit"

	// EventHttpRun HTTP 请求开始处理前触发。
	// 对应 ThinkPHP 的 HttpRun 事件，在全局中间件执行之前分发。
	EventHttpRun = "framework.HttpRun"

	// EventHttpEnd HTTP 请求处理完成后触发。
	// 对应 ThinkPHP 的 HttpEnd 事件，在响应发送之后分发。
	EventHttpEnd = "framework.HttpEnd"

	// EventRouteLoaded 路由加载完成后触发。
	// 对应 ThinkPHP 的 RouteLoaded 事件。
	EventRouteLoaded = "framework.RouteLoaded"
)

// AppInitEvent 应用初始化事件。
type AppInitEvent struct {
	*SimpleEvent
}

// NewAppInitEvent 创建应用初始化事件。
func NewAppInitEvent() *AppInitEvent {
	return &AppInitEvent{SimpleEvent: &SimpleEvent{name: EventAppInit}}
}

// HttpRunEvent HTTP 请求开始事件。
type HttpRunEvent struct {
	*SimpleEvent
}

// NewHttpRunEvent 创建 HTTP 运行事件。
func NewHttpRunEvent() *HttpRunEvent {
	return &HttpRunEvent{SimpleEvent: &SimpleEvent{name: EventHttpRun}}
}

// HttpEndEvent HTTP 请求结束事件，携带响应状态码。
type HttpEndEvent struct {
	*SimpleEvent
	StatusCode int
}

// NewHttpEndEvent 创建 HTTP 结束事件。
func NewHttpEndEvent(statusCode int) *HttpEndEvent {
	return &HttpEndEvent{
		SimpleEvent: &SimpleEvent{name: EventHttpEnd},
		StatusCode:  statusCode,
	}
}

// RouteLoadedEvent 路由加载完成事件。
type RouteLoadedEvent struct {
	*SimpleEvent
}

// NewRouteLoadedEvent 创建路由加载完成事件。
func NewRouteLoadedEvent() *RouteLoadedEvent {
	return &RouteLoadedEvent{SimpleEvent: &SimpleEvent{name: EventRouteLoaded}}
}
