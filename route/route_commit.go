package route

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// AddWithCommit 在路由校验和冲突检查通过后提交关联契约，失败时不注册路由。
// commit 在路由注册锁内执行，不得重新调用该路由器；返回错误时必须保持自身状态不变。
func (r *Router) AddWithCommit(method, path string, handler HandlerFunc, commit func(RouteInfo) error, handlers ...middleware.Handler) (*Route, error) {
	if r == nil {
		return nil, ErrInvalidRoute
	}
	return r.rootGroup().AddWithCommit(method, path, handler, commit, handlers...)
}

// AddWithCommit 向关联契约提供合并分组前缀、中间件和域名后的最终路由信息。
// commit 与 Router.AddWithCommit 遵守同一锁顺序和错误原子性约定。
func (g *Group) AddWithCommit(method, path string, handler HandlerFunc, commit func(RouteInfo) error, handlers ...middleware.Handler) (*Route, error) {
	if g == nil || g.router == nil || commit == nil {
		return nil, ErrInvalidRoute
	}
	definitions := []routeDefinition{g.newDefinition(method, path, handler, handlers)}
	routes, err := g.router.registerDefinitionsWithCommit(definitions, commit)
	if err != nil {
		return nil, err
	}
	return routes[0], nil
}

// registerDefinitionsWithCommit 将所有可能失败的检查放在关联状态发布之前。
// 当前关联提交仅用于单条路由，普通资源路由仍按整批校验、整批发布执行。
func (r *Router) registerDefinitionsWithCommit(definitions []routeDefinition, commit func(RouteInfo) error) ([]*Route, error) {
	if commit != nil && len(definitions) != 1 {
		return nil, ErrInvalidRoute
	}
	prepared := make([]*Route, 0, len(definitions))
	for _, definition := range definitions {
		method, err := normalizeRouteMethod(definition.method)
		if err != nil {
			return nil, err
		}
		path, _, err := canonicalRoutePath(definition.path)
		if err != nil {
			return nil, err
		}
		if err = validateRouteHandler(definition.handler); err != nil {
			return nil, err
		}
		for index, entry := range definition.middlewares {
			if entry.handler == nil {
				return nil, fmt.Errorf("%w: 第 %d 项为空", ErrInvalidRouteMiddleware, index+1)
			}
		}
		domain, err := normalizeAndValidateRouteDomain(definition.domain)
		if err != nil {
			return nil, err
		}
		ext := ""
		extensionRequired := false
		if definition.ext != "" {
			ext, err = normalizeRouteExtension(definition.ext)
			if err != nil {
				return nil, err
			}
			extensionRequired = definition.extensionRequired
		}
		patterns := make(map[string]string)
		compiled := make(map[string]*regexp.Regexp)
		for param, pattern := range definition.patterns {
			if !routeHasParam(path, param) {
				continue
			}
			regex, compileErr := compileRoutePattern(param, pattern)
			if compileErr != nil {
				return nil, compileErr
			}
			patterns[param] = pattern
			compiled[param] = regex
		}
		_, jsonContract := definition.handler.(*JSONHandler)
		prepared = append(prepared, &Route{
			router:             r,
			method:             method,
			path:               path,
			handler:            definition.handler,
			middlewares:        slices.Clone(definition.middlewares),
			middlewarePipeline: buildMiddlewarePipeline(definition.middlewares),
			patterns:           patterns,
			compiled:           compiled,
			explicitPatterns:   make(map[string]bool),
			domain:             domain,
			ext:                ext,
			extensionRequired:  extensionRequired,
			jsonContract:       jsonContract,
			trailingSlash:      routeHasTrailingSlash(definition.path),
		})
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return nil, ErrRouterFrozen
	}
	for _, candidate := range prepared {
		if duplicate := r.findDuplicateLocked(candidate, nil, prepared); duplicate != nil {
			return nil, fmt.Errorf("%w: %s %s domain=%q", ErrDuplicateRoute, candidate.method, candidate.path, candidate.domain)
		}
	}
	if commit != nil {
		candidate := prepared[0]
		if err := commit(RouteInfo{
			Method:            candidate.method,
			Path:              candidate.path,
			Handler:           candidate.handler,
			Domain:            candidate.domain,
			Extension:         candidate.ext,
			ExtensionRequired: candidate.extensionRequired,
			MiddlewareCount:   len(candidate.middlewares),
		}); err != nil {
			return nil, err
		}
	}
	for _, registered := range prepared {
		registered.order = len(r.routes)
		r.routes = append(r.routes, registered)
	}
	return prepared, nil
}
