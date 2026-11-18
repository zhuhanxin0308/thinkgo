package route

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

var nextRouteMiddlewareID atomic.Uint64

type routeMiddleware struct {
	id      uint64
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
	next := g.clone()
	next.middlewares = entries
	return next, nil
}

// WithMiddleware 派生追加匿名中间件的分组，原分组仍可独立注册路由。
func (g *Group) WithMiddleware(handlers ...middleware.Handler) (*Group, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	if err := validateMiddleware(handlers); err != nil {
		return nil, err
	}
	g.router.mu.RLock()
	defer g.router.mu.RUnlock()
	if g.router.frozen {
		return nil, ErrRouterFrozen
	}
	next := g.clone()
	next.middlewares = mergeMiddleware(g.middlewares, handlers)
	return next, nil
}

// WithoutNamedMiddleware 派生移除指定命名中间件的分组。
func (g *Group) WithoutNamedMiddleware(names ...string) (*Group, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	g.router.mu.RLock()
	defer g.router.mu.RUnlock()
	if g.router.frozen {
		return nil, ErrRouterFrozen
	}
	working, err := removeNamedMiddlewares(g.middlewares, names)
	if err != nil {
		return nil, err
	}
	next := g.clone()
	next.middlewares = working
	return next, nil
}

// WithoutMiddleware 无法从 Go 函数值确认注册身份，非空请求会返回错误。
func (g *Group) WithoutMiddleware(targets ...middleware.Handler) (*Group, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	if len(targets) == 0 {
		return g.clone(), nil
	}
	if err := validateMiddleware(targets); err != nil {
		return nil, err
	}
	g.router.mu.RLock()
	defer g.router.mu.RUnlock()
	if g.router.frozen {
		return nil, ErrRouterFrozen
	}
	return nil, fmt.Errorf("%w: 无法确认函数的注册身份，请按名称移除", ErrInvalidRouteMiddleware)
}

// ApplyMiddlewareToRoutes 将分组后置链式中间件原子回填到已注册组内路由，并返回更新后的分组作用域。
func (g *Group) ApplyMiddlewareToRoutes(routes []*Route, handlers ...middleware.Handler) (*Group, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	if err := validateMiddleware(handlers); err != nil {
		return nil, err
	}
	g.router.mu.Lock()
	defer g.router.mu.Unlock()
	if g.router.frozen {
		return nil, ErrRouterFrozen
	}
	added := newMiddlewareEntries(handlers)
	next := g.clone()
	next.middlewares = append(slices.Clone(g.middlewares), added...)
	if len(added) == 0 {
		return next, nil
	}
	for _, registered := range uniqueActiveRoutes(routes) {
		insertAt := groupMiddlewareInsertionIndex(registered.middlewares, g.middlewares)
		registered.middlewares = slices.Insert(slices.Clone(registered.middlewares), insertAt, added...)
		registered.middlewarePipeline = buildMiddlewarePipeline(registered.middlewares)
	}
	return next, nil
}

// ApplyNamedMiddlewareToRoutes 将分组后置命名中间件原子回填到已注册组内路由，并返回更新后的分组作用域。
func (g *Group) ApplyNamedMiddlewareToRoutes(routes []*Route, name string, handler middleware.Handler) (*Group, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	g.router.mu.Lock()
	defer g.router.mu.Unlock()
	if g.router.frozen {
		return nil, ErrRouterFrozen
	}
	nextEntries, err := appendNamedMiddleware(slices.Clone(g.middlewares), name, handler)
	if err != nil {
		return nil, err
	}
	added := nextEntries[len(nextEntries)-1]
	active := uniqueActiveRoutes(routes)
	for _, registered := range active {
		if namedMiddlewareIndex(registered.middlewares, added.name) >= 0 {
			return nil, fmt.Errorf("%w: 中间件名称 %q 无效或已使用，处理函数不能为空", ErrInvalidRouteMiddleware, added.name)
		}
	}
	for _, registered := range active {
		insertAt := groupMiddlewareInsertionIndex(registered.middlewares, g.middlewares)
		registered.middlewares = slices.Insert(slices.Clone(registered.middlewares), insertAt, added)
		registered.middlewarePipeline = buildMiddlewarePipeline(registered.middlewares)
	}
	next := g.clone()
	next.middlewares = nextEntries
	return next, nil
}

// ApplyWithoutNamedMiddlewareToRoutes 按注册名称原子移除分组及已注册组内路由的中间件。
func (g *Group) ApplyWithoutNamedMiddlewareToRoutes(routes []*Route, names ...string) (*Group, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	g.router.mu.Lock()
	defer g.router.mu.Unlock()
	if g.router.frozen {
		return nil, ErrRouterFrozen
	}
	workingGroup, err := removeNamedMiddlewares(g.middlewares, names)
	if err != nil {
		return nil, err
	}
	for _, registered := range uniqueActiveRoutes(routes) {
		updated := slices.Clone(registered.middlewares)
		for _, name := range names {
			trimmed := strings.TrimSpace(name)
			if index := namedMiddlewareIndex(updated, trimmed); index >= 0 {
				updated = slices.Delete(updated, index, index+1)
			}
		}
		registered.middlewares = updated
		registered.middlewarePipeline = buildMiddlewarePipeline(updated)
	}
	next := g.clone()
	next.middlewares = workingGroup
	return next, nil
}

func removeNamedMiddlewares(entries []routeMiddleware, names []string) ([]routeMiddleware, error) {
	working := slices.Clone(entries)
	for _, name := range names {
		name = strings.TrimSpace(name)
		index := namedMiddlewareIndex(working, name)
		if !isValidRouteName(name) || index < 0 {
			return nil, fmt.Errorf("%w: 找不到待移除的中间件 %q", ErrInvalidRouteMiddleware, name)
		}
		working = slices.Delete(working, index, index+1)
	}
	return working, nil
}

func groupMiddlewareInsertionIndex(routeEntries, groupEntries []routeMiddleware) int {
	if len(groupEntries) == 0 || len(routeEntries) == 0 {
		return 0
	}
	groupIDs := make(map[uint64]struct{}, len(groupEntries))
	for _, entry := range groupEntries {
		if entry.id != 0 {
			groupIDs[entry.id] = struct{}{}
		}
	}
	insertAt := 0
	for index, entry := range routeEntries {
		if _, exists := groupIDs[entry.id]; exists {
			insertAt = index + 1
		}
	}
	return insertAt
}

func uniqueActiveRoutes(routes []*Route) []*Route {
	if len(routes) == 0 {
		return nil
	}
	seen := make(map[*Route]struct{}, len(routes))
	active := make([]*Route, 0, len(routes))
	for _, registered := range routes {
		if registered == nil || registered.removed {
			continue
		}
		if _, exists := seen[registered]; exists {
			continue
		}
		seen[registered] = struct{}{}
		active = append(active, registered)
	}
	return active
}

func appendNamedMiddleware(entries []routeMiddleware, name string, handler middleware.Handler) ([]routeMiddleware, error) {
	name = strings.TrimSpace(name)
	if !isValidRouteName(name) || handler == nil || namedMiddlewareIndex(entries, name) >= 0 {
		return nil, fmt.Errorf("%w: 中间件名称 %q 无效或已使用，处理函数不能为空", ErrInvalidRouteMiddleware, name)
	}
	return append(entries, routeMiddleware{id: nextRouteMiddlewareID.Add(1), name: name, handler: handler}), nil
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

func newMiddlewareEntries(current []middleware.Handler) []routeMiddleware {
	if len(current) == 0 {
		return nil
	}
	entries := make([]routeMiddleware, len(current))
	for index, handler := range current {
		entries[index] = routeMiddleware{id: nextRouteMiddlewareID.Add(1), handler: handler}
	}
	return entries
}

func mergeMiddleware(parent []routeMiddleware, current []middleware.Handler) []routeMiddleware {
	merged := make([]routeMiddleware, 0, len(parent)+len(current))
	merged = append(merged, parent...)
	merged = append(merged, newMiddlewareEntries(current)...)
	return merged
}
