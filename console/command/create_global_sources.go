package command

// projectGlobalSources 提供项目级扩展入口，保持生成工程与原生多应用宿主的装配约定一致。
var projectGlobalSources = map[string]string{
	"event.go": `package app

import "github.com/zhuhanxin0308/thinkgo/v3"

// Events 定义项目级事件，所有应用继承这些监听关系。
func Events() framework.EventDefinition { return framework.EventDefinition{} }
`,
	"middleware.go": `package app

import "github.com/zhuhanxin0308/thinkgo/v3/middleware"

// Middleware 返回项目级中间件，执行顺序位于各应用中间件之外。
func Middleware() []middleware.Handler { return nil }
`,
	"service.go": `package app

// Services 注册在全部业务应用之间共享的项目服务。
func Services() []interface{} { return []interface{}{&AppService{}} }
`,
	"app_service.go": `package app

import "github.com/zhuhanxin0308/thinkgo/v3"

// AppService 是项目共享服务入口，可按业务需要实现 Register 和 Boot。
type AppService struct { framework.Service }

// Register 注册项目共享依赖，默认项目不额外覆盖容器。
func (*AppService) Register() {}

// Boot 在共享依赖注册完成后执行业务初始化。
func (*AppService) Boot() {}
`,
	"request.go": `package app

import (
    "net/http"
    "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// Request 保留框架请求类型，项目可在工厂中配置公共请求行为。
type Request = context.Request

// NewRequest 创建当前请求对象，生命周期由框架请求作用域管理。
func NewRequest(raw *http.Request, options ...context.RequestOption) (*Request, error) {
    return context.NewRequest(raw, options...)
}
`,
	"provider.go": `package app

import "github.com/zhuhanxin0308/thinkgo/v3"

// Providers 将项目的请求和异常处理工厂绑定到容器。
func Providers() map[string]interface{} {
    return map[string]interface{}{
        string(framework.ServiceRequest): NewRequest,
        string(framework.ServiceExceptionHandle): NewExceptionHandle,
    }
}
`,
	"exception_handle.go": `package app

import (
    "fmt"
    "github.com/zhuhanxin0308/thinkgo/v3"
    "github.com/zhuhanxin0308/thinkgo/v3/exception"
    frameworkLog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

// ExceptionHandle 提供项目级异常扩展入口，默认使用框架报告与渲染行为。
type ExceptionHandle struct { exception.Handle }

// NewExceptionHandle 从当前应用容器获取日志和模板上下文，避免跨应用共享状态。
func NewExceptionHandle(container *framework.Container) (exception.Handler, error) {
    value, err := container.Make(string(framework.ServiceApp))
    if err != nil { return nil, err }
    application, ok := value.(*framework.App)
    if !ok || application == nil { return nil, fmt.Errorf("应用实例类型错误: %T", value) }
    value, err = container.Make(string(framework.ServiceLog))
    if err != nil { return nil, err }
    logger, ok := value.(*frameworkLog.Log)
    if !ok || logger == nil { return nil, fmt.Errorf("日志实例类型错误: %T", value) }
    return &ExceptionHandle{Handle: exception.Handle{App: application, Log: logger, TplDir: application.ExceptionTemplatePath()}}, nil
}
`,
	"base_controller.go": `package app

import "github.com/zhuhanxin0308/thinkgo/v3"

// BaseController 保留项目级控制器扩展入口，各应用也有自己的基础控制器。
type BaseController struct { framework.Controller }
`,
}
