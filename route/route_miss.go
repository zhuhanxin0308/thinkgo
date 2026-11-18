package route

import (
	"fmt"
	"regexp"
	"slices"
)

// Miss 注册唯一的通用未命中处理器，保留底层 Router 的显式重复检查。
func (r *Router) Miss(handler HandlerFunc) (*Route, error) {
	if err := validateRouteHandler(handler); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return nil, ErrRouterFrozen
	}
	if r.missRoute != nil {
		return nil, fmt.Errorf("%w: Miss 路由只能注册一次", ErrDuplicateRoute)
	}
	registered := newMissRoute(r, anyMethod, handler, nil)
	registered.order = len(r.missList)
	r.missRoute = registered
	if r.missRoutes == nil {
		r.missRoutes = make(map[string]*Route)
	}
	r.missRoutes[anyMethod] = registered
	r.missList = append(r.missList, registered)
	return registered, nil
}

// SetMiss 按请求方法注册或替换 MISS 路由，对应 ThinkPHP RuleGroup::miss。
func (r *Router) SetMiss(method string, handler HandlerFunc) (*Route, error) {
	if r == nil {
		return nil, ErrInvalidRoute
	}
	return r.rootGroup().SetMiss(method, handler)
}

// SetMiss 在当前分组作用域下按请求方法注册或替换 MISS 路由，继承前缀、域名、扩展名与组中间件。
func (g *Group) SetMiss(method string, handler HandlerFunc) (*Route, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	if err := validateRouteHandler(handler); err != nil {
		return nil, err
	}
	normalized, err := normalizeRouteMethod(method)
	if err != nil {
		return nil, err
	}
	r := g.router
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return nil, ErrRouterFrozen
	}
	registered := newMissRoute(r, normalized, handler, g)
	registered.order = len(r.missList)
	r.missList = append(r.missList, registered)
	if g.prefix == "" && g.domain == "" && !g.extensionRequired {
		if r.missRoutes == nil {
			r.missRoutes = make(map[string]*Route)
		}
		r.missRoutes[normalized] = registered
		if normalized == anyMethod {
			r.missRoute = registered
		}
	}
	return registered, nil
}

func newMissRoute(router *Router, method string, handler HandlerFunc, group *Group) *Route {
	registered := &Route{
		router:           router,
		method:           method,
		path:             "__miss__",
		handler:          handler,
		patterns:         make(map[string]string),
		compiled:         make(map[string]*regexp.Regexp),
		explicitPatterns: make(map[string]bool),
		isMiss:           true,
	}
	if group != nil {
		registered.missPrefix = group.prefix
		registered.domain = group.domain
		registered.domainDepth = group.domainDepth
		registered.ext = group.ext
		registered.extDepth = group.extDepth
		registered.extensionRequired = group.extensionRequired
		registered.middlewares = slices.Clone(group.middlewares)
		registered.middlewarePipeline = buildMiddlewarePipeline(group.middlewares)
		if group.prefix != "" && len(group.patterns) > 0 {
			registered.patternDepth = make(map[string]int)
			for param, pattern := range group.patterns {
				if !routeHasParam(group.prefix, param) {
					continue
				}
				if regex, err := compileRoutePattern(param, pattern); err == nil {
					registered.patterns[param] = pattern
					registered.compiled[param] = regex
					registered.patternDepth[param] = group.patternDepth[param]
				}
			}
		}
	}
	return registered
}
