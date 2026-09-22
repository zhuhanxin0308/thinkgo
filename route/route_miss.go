package route

import (
	"fmt"
	"regexp"

	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
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
	r.missRoute = newMissRoute(r, anyMethod, handler)
	return r.missRoute, nil
}

// SetMiss 按请求方法注册或替换 MISS 路由，对应 ThinkPHP RuleGroup::miss。
func (r *Router) SetMiss(method string, handler HandlerFunc) (*Route, error) {
	if err := validateRouteHandler(handler); err != nil {
		return nil, err
	}
	normalized, err := normalizeRouteMethod(method)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return nil, ErrRouterFrozen
	}
	registered := newMissRoute(r, normalized, handler)
	if r.missRoutes == nil {
		r.missRoutes = make(map[string]*Route)
	}
	r.missRoutes[normalized] = registered
	if normalized == anyMethod {
		r.missRoute = registered
	}
	return registered, nil
}

func newMissRoute(router *Router, method string, handler HandlerFunc) *Route {
	return &Route{
		router:      router,
		method:      method,
		path:        "__miss__",
		handler:     handler,
		middlewares: []middleware.Handler{},
		patterns:    make(map[string]string),
		compiled:    make(map[string]*regexp.Regexp),
	}
}
