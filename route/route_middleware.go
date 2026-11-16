package route

import (
	"fmt"
	"slices"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

type routeMiddleware struct {
	name    string
	handler middleware.Handler
}

// WithoutMiddleware 无法从 Go 函数值确认注册身份，非空请求会返回错误。
// Deprecated: 注册命名中间件并使用 WithoutNamedMiddleware。
func (r *Route) WithoutMiddleware(targets ...middleware.Handler) error {
	router, err := r.mutableRouter()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	if err = validateMiddleware(targets); err != nil {
		return err
	}
	router.mu.RLock()
	defer router.mu.RUnlock()
	if router.frozen {
		return ErrRouterFrozen
	}
	return fmt.Errorf("%w: 无法确认函数的注册身份，请按名称移除", ErrInvalidRouteMiddleware)
}

// WithoutNamedMiddleware 按注册名称原子移除当前路由的中间件，保留其余中间件的顺序。
func (r *Route) WithoutNamedMiddleware(names ...string) error {
	router, err := r.mutableRouter()
	if err != nil || len(names) == 0 {
		return err
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.frozen {
		return ErrRouterFrozen
	}
	working := slices.Clone(r.middlewares)
	for _, name := range names {
		name = strings.TrimSpace(name)
		index := namedMiddlewareIndex(working, name)
		if !isValidRouteName(name) || index < 0 {
			return fmt.Errorf("%w: 找不到待移除的中间件 %q", ErrInvalidRouteMiddleware, name)
		}
		working = slices.Delete(working, index, index+1)
	}
	r.middlewares = working
	r.middlewarePipeline = buildMiddlewarePipeline(working)
	return nil
}

// WithNamedMiddleware 在冻结前以唯一名称为当前路由追加中间件。
func (r *Route) WithNamedMiddleware(name string, handler middleware.Handler) error {
	router, err := r.mutableRouter()
	if err != nil {
		return err
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.frozen {
		return ErrRouterFrozen
	}
	entries, err := appendNamedMiddleware(r.middlewares, name, handler)
	if err != nil {
		return err
	}
	r.middlewares = entries
	r.middlewarePipeline = buildMiddlewarePipeline(entries)
	return nil
}

// WithNamedMiddleware 派生继承命名中间件的分组，原分组仍可独立注册路由。
func (g *Group) WithNamedMiddleware(name string, handler middleware.Handler) (*Group, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	g.router.mu.RLock()
	defer g.router.mu.RUnlock()
	if g.router.frozen {
		return nil, ErrRouterFrozen
	}
	entries, err := appendNamedMiddleware(slices.Clone(g.middlewares), name, handler)
	if err != nil {
		return nil, err
	}
	next := *g
	next.middlewares = entries
	return &next, nil
}

func appendNamedMiddleware(entries []routeMiddleware, name string, handler middleware.Handler) ([]routeMiddleware, error) {
	name = strings.TrimSpace(name)
	if !isValidRouteName(name) || handler == nil || namedMiddlewareIndex(entries, name) >= 0 {
		return nil, fmt.Errorf("%w: 中间件名称 %q 无效或已使用，处理函数不能为空", ErrInvalidRouteMiddleware, name)
	}
	return append(entries, routeMiddleware{name: name, handler: handler}), nil
}

func namedMiddlewareIndex(entries []routeMiddleware, name string) int {
	return slices.IndexFunc(entries, func(entry routeMiddleware) bool { return entry.name == name })
}

// WithMiddleware 在路由冻结前追加中间件。
func (r *Route) WithMiddleware(handlers ...middleware.Handler) error {
	router, err := r.mutableRouter()
	if err != nil {
		return err
	}
	if len(handlers) == 0 {
		return nil
	}
	if err = validateMiddleware(handlers); err != nil {
		return err
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.frozen {
		return ErrRouterFrozen
	}
	r.middlewares = mergeMiddleware(r.middlewares, handlers)
	r.middlewarePipeline = buildMiddlewarePipeline(r.middlewares)
	return nil
}

// Middlewares 返回防御性副本。
func (r *Route) Middlewares() []middleware.Handler {
	if r == nil {
		return nil
	}
	if r.router != nil {
		r.router.mu.RLock()
		defer r.router.mu.RUnlock()
	}
	if len(r.middlewares) == 0 {
		return nil
	}
	handlers := make([]middleware.Handler, len(r.middlewares))
	for index, entry := range r.middlewares {
		handlers[index] = entry.handler
	}
	return handlers
}

// HasMiddleware 判断路由是否配置中间件，避免热路径为确认空管道复制中间件切片。
func (r *Route) HasMiddleware() bool {
	if r == nil {
		return false
	}
	if r.router != nil {
		r.router.mu.RLock()
		hasMiddleware := r.middlewarePipeline != nil
		r.router.mu.RUnlock()
		return hasMiddleware
	}
	return len(r.middlewares) > 0
}

// ExecuteMiddleware 使用路由注册阶段构建的不可变管道执行中间件，避免每个请求重复复制和装配管道。
func (r *Route) ExecuteMiddleware(request *context.Request, destination func(*context.Request) *context.Response) *context.Response {
	if r == nil {
		return destination(request)
	}
	if r.router != nil {
		r.router.mu.RLock()
		pipeline := r.middlewarePipeline
		r.router.mu.RUnlock()
		if pipeline != nil {
			return pipeline.Then(request, destination)
		}
	}
	return destination(request)
}

// buildMiddlewarePipeline 在路由注册或修改时一次性装配管道，运行期间只读共享。
func buildMiddlewarePipeline(handlers []routeMiddleware) *middleware.Pipeline {
	if len(handlers) == 0 {
		return nil
	}
	pipeline := middleware.NewPipeline()
	for _, entry := range handlers {
		pipeline.Pipe(entry.handler)
	}
	return pipeline
}

func validateMiddleware(handlers []middleware.Handler) error {
	for index, handler := range handlers {
		if handler == nil {
			return fmt.Errorf("%w: 第 %d 项为空", ErrInvalidRouteMiddleware, index+1)
		}
	}
	return nil
}

func mergeMiddleware(parent []routeMiddleware, current []middleware.Handler) []routeMiddleware {
	merged := make([]routeMiddleware, 0, len(parent)+len(current))
	merged = append(merged, parent...)
	for _, handler := range current {
		merged = append(merged, routeMiddleware{handler: handler})
	}
	return merged
}
