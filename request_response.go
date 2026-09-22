package framework

import "github.com/zhuhanxin0308/thinkgo/v3/context"

// Request 是框架顶层请求类型，对应 ThinkPHP 的 think\Request。
type Request = context.Request

// Response 是框架顶层响应类型，对应 ThinkPHP 的 think\Response。
type Response = context.Response

// NewResponse 创建框架响应对象，业务控制器和路由闭包无需导入 context 子包。
func NewResponse() *Response {
	return context.NewResponse()
}
