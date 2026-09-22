package framework

import (
	"context"
	"errors"
)

// ErrInvalidContainerResolutionContext 表示显式服务解析没有提供有效上下文。
var ErrInvalidContainerResolutionContext = errors.New("服务解析上下文不能为空")

// Context 返回当前依赖解析链的上下文；未显式指定时使用后台上下文。
// 上下文保存在本次解析副本中，不修改根容器或其它并发解析。
func (container *Container) Context() context.Context {
	if container != nil && container.resolution != nil && container.resolution.context != nil {
		return container.resolution.context
	}
	return context.Background()
}

// MakeContext 在独立的解析上下文中构造服务，嵌套工厂沿用同一上下文。
func (container *Container) MakeContext(ctx context.Context, name string, params ...interface{}) (interface{}, error) {
	if ctx == nil {
		return nil, ErrInvalidContainerResolutionContext
	}
	if container == nil {
		return nil, ErrServiceUnavailable
	}
	previous := container.currentResolution()
	// 上下文只随解析链传递；具体服务决定是否因取消停止工作。
	// 响应发送及终结阶段仍可能需要解析清理服务，不能在容器统一提前中断。
	next := &resolution{stack: previous.stack, context: ctx}
	return container.makeWithResolution(next, name, params...)
}

// MakeContext 从当前应用解析服务，并保持应用关闭后的访问限制。
func (app *App) MakeContext(ctx context.Context, name string, params ...interface{}) (interface{}, error) {
	if ctx == nil {
		return nil, ErrInvalidContainerResolutionContext
	}
	return app.resolveService(ServiceName(name), ctx, params...)
}

// MakeContext 在当前请求或任务作用域中解析服务，不改变作用域共享状态。
func (scope *ContainerScope) MakeContext(ctx context.Context, name string, params ...interface{}) (interface{}, error) {
	if scope == nil || scope.container == nil {
		return nil, ErrContainerScopeClosed
	}
	scope.state.lock.Lock()
	closed := scope.state.closing || scope.state.closed
	scope.state.lock.Unlock()
	if closed {
		return nil, ErrContainerScopeClosed
	}
	return scope.container.MakeContext(ctx, name, params...)
}
