package openapi

import (
	"net/http"

	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// API 将一个实际路由作用域与注册表关联，接口只需声明路径和真实处理器。
type API struct {
	registry *Registry
	target   RouteRegistrar
}

// Routes 绑定路由器、分组或应用门面，继续保留其前缀和中间件。
func (registry *Registry) Routes(target RouteRegistrar) *API {
	return &API{registry: registry, target: target}
}

// Handle 支持创建状态码等显式配置，其他元数据仍从源码注释补充。
func (api *API) Handle(operation Operation, callback any, handlers ...middleware.Handler) error {
	if api == nil {
		return ErrInvalidOperation
	}
	return Handle(api.target, api.registry, operation, callback, handlers...)
}

// Get 注册读取接口。
func (api *API) Get(path string, callback any, handlers ...middleware.Handler) error {
	return api.Handle(Operation{Method: http.MethodGet, Path: path}, callback, handlers...)
}

// Post 注册提交接口；需要 201 等状态时通过 Handle 显式声明。
func (api *API) Post(path string, callback any, handlers ...middleware.Handler) error {
	return api.Handle(Operation{Method: http.MethodPost, Path: path}, callback, handlers...)
}

// Put 注册替换接口。
func (api *API) Put(path string, callback any, handlers ...middleware.Handler) error {
	return api.Handle(Operation{Method: http.MethodPut, Path: path}, callback, handlers...)
}

// Patch 注册部分更新接口。
func (api *API) Patch(path string, callback any, handlers ...middleware.Handler) error {
	return api.Handle(Operation{Method: http.MethodPatch, Path: path}, callback, handlers...)
}

// Delete 注册删除接口。
func (api *API) Delete(path string, callback any, handlers ...middleware.Handler) error {
	return api.Handle(Operation{Method: http.MethodDelete, Path: path}, callback, handlers...)
}

// Head 注册无响应体的元信息接口。
func (api *API) Head(path string, callback any, handlers ...middleware.Handler) error {
	return api.Handle(Operation{Method: http.MethodHead, Path: path}, callback, handlers...)
}

// Options 注册能力查询接口。
func (api *API) Options(path string, callback any, handlers ...middleware.Handler) error {
	return api.Handle(Operation{Method: http.MethodOptions, Path: path}, callback, handlers...)
}
