package route

import (
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"thinkgo/framework/context"
	"thinkgo/framework/middleware"
)

// HandlerFunc 描述路由处理器，可以是闭包，也可以是 Controller@Action 字符串。
type HandlerFunc interface{}

// Route 表示单条路由规则。
type Route struct {
	Method           string
	Path             string
	Handler          HandlerFunc
	Middleware       []middleware.Handler
	// Auto 标记该路由由自动路由解析生成（URL → Controller@Action），
	// 分发层会对其施加更严格的方法可达性限制，避免暴露控制器基类方法。
	Auto             bool
	name             string
	patterns         map[string]string
	compiledPatterns map[string]*regexp.Regexp
	pathParts        []string // 注册时预切分的路径片段，避免每次请求重复切分
	domain           string
	ext              string
	router           *Router
}

// Router 管理应用内全部路由，并维护静态、动态、命名路由索引。
type Router struct {
	routes          []*Route
	staticRoutes    map[string]map[string]*Route
	dynamicRoutes   map[string][]*Route
	namedRoutes     map[string]*Route
	groups          []string
	groupDomains    []string
	groupMiddleware [][]middleware.Handler
	missRoute       *Route
	// 自动路由配置（对应 ThinkPHP 的 URL 解析模式）
	autoRoute         bool   // 是否启用自动路由
	defaultController string // 默认控制器名（对应 ThinkPHP 的 default_controller）
	defaultAction     string // 默认动作名（对应 ThinkPHP 的 default_action）
}

// ResourceRoute 表示一组 RESTful 资源路由，支持 only/except 精细裁剪。
type ResourceRoute struct {
	router *Router
	routes map[string]*Route
}

// resourceDefinitions 定义 RESTful 资源路由的标准动作集合。
// 动作名与 ThinkPHP 8 保持一致：save（创建）、read（详情）。
var resourceDefinitions = []struct {
	action     string
	method     string
	pathSuffix string
	handler    string
}{
	{action: "index", method: "GET", pathSuffix: "", handler: "@Index"},
	{action: "create", method: "GET", pathSuffix: "/create", handler: "@Create"},
	{action: "save", method: "POST", pathSuffix: "", handler: "@Save"},
	{action: "read", method: "GET", pathSuffix: "/:id", handler: "@Read"},
	{action: "edit", method: "GET", pathSuffix: "/:id/edit", handler: "@Edit"},
	{action: "update", method: "PUT", pathSuffix: "/:id", handler: "@Update"},
	{action: "delete", method: "DELETE", pathSuffix: "/:id", handler: "@Delete"},
}

// NewRouter 创建路由管理器。
func NewRouter() *Router {
	return &Router{
		routes:            make([]*Route, 0),
		staticRoutes:      make(map[string]map[string]*Route),
		dynamicRoutes:     make(map[string][]*Route),
		namedRoutes:       make(map[string]*Route),
		groups:            make([]string, 0),
		groupDomains:      make([]string, 0),
		groupMiddleware:   make([][]middleware.Handler, 0),
		defaultController: "Index",
		defaultAction:     "index",
	}
}

// EnableAutoRoute 启用自动路由。
// 对应 ThinkPHP 的 url_route_must = false 时的 URL 解析模式。
// URL 路径将自动映射到 Controller@Action，无需手动注册路由。
func (r *Router) EnableAutoRoute(enable bool) *Router {
	r.autoRoute = enable
	return r
}

// SetDefaultController 设置自动路由的默认控制器名。
func (r *Router) SetDefaultController(name string) *Router {
	r.defaultController = name
	return r
}

// SetDefaultAction 设置自动路由的默认动作名。
func (r *Router) SetDefaultAction(name string) *Router {
	r.defaultAction = name
	return r
}

// Add 注册新路由。
func (r *Router) Add(method, path string, handler HandlerFunc, middlewares ...middleware.Handler) *Route {
	fullPath := path
	if len(r.groups) > 0 {
		fullPath = strings.Join(r.groups, "") + path
	}
	if !strings.HasPrefix(fullPath, "/") {
		fullPath = "/" + fullPath
	}

	allMiddleware := make([]middleware.Handler, 0)
	for _, groupHandlers := range r.groupMiddleware {
		allMiddleware = append(allMiddleware, groupHandlers...)
	}
	allMiddleware = append(allMiddleware, middlewares...)

	route := &Route{
		Method:           method,
		Path:             fullPath,
		Handler:          handler,
		Middleware:       allMiddleware,
		patterns:         make(map[string]string),
		compiledPatterns: make(map[string]*regexp.Regexp),
		router:           r,
	}
	if domain := r.currentGroupDomain(); domain != "" {
		route.domain = domain
	}

	r.appendRoute(route)
	return route
}

// Get 注册 GET 路由。
func (r *Router) Get(path string, handler HandlerFunc, middlewares ...middleware.Handler) *Route {
	return r.Add("GET", path, handler, middlewares...)
}

// Post 注册 POST 路由。
func (r *Router) Post(path string, handler HandlerFunc, middlewares ...middleware.Handler) *Route {
	return r.Add("POST", path, handler, middlewares...)
}

// Put 注册 PUT 路由。
func (r *Router) Put(path string, handler HandlerFunc, middlewares ...middleware.Handler) *Route {
	return r.Add("PUT", path, handler, middlewares...)
}

// Delete 注册 DELETE 路由。
func (r *Router) Delete(path string, handler HandlerFunc, middlewares ...middleware.Handler) *Route {
	return r.Add("DELETE", path, handler, middlewares...)
}

// Patch 注册 PATCH 路由。
func (r *Router) Patch(path string, handler HandlerFunc, middlewares ...middleware.Handler) *Route {
	return r.Add("PATCH", path, handler, middlewares...)
}

// Options 注册 OPTIONS 路由。
func (r *Router) Options(path string, handler HandlerFunc, middlewares ...middleware.Handler) *Route {
	return r.Add("OPTIONS", path, handler, middlewares...)
}

// Head 注册 HEAD 路由。
func (r *Router) Head(path string, handler HandlerFunc, middlewares ...middleware.Handler) *Route {
	return r.Add("HEAD", path, handler, middlewares...)
}

// Any 注册匹配全部 HTTP 方法的路由。
func (r *Router) Any(path string, handler HandlerFunc, middlewares ...middleware.Handler) *Route {
	return r.Add("*", path, handler, middlewares...)
}

// Group 创建带前缀的路由组。
func (r *Router) Group(prefix string, fn func(), middlewares ...middleware.Handler) {
	r.enterGroup(prefix, "", middlewares...)
	defer r.leaveGroup()
	fn()
}

// Domain 在同一域名下批量注册路由。
func (r *Router) Domain(domain string, fn func(), middlewares ...middleware.Handler) {
	r.enterGroup("", domain, middlewares...)
	defer r.leaveGroup()
	fn()
}

// DomainGroup 在同一域名和前缀下批量注册路由。
func (r *Router) DomainGroup(domain string, prefix string, fn func(), middlewares ...middleware.Handler) {
	r.enterGroup(prefix, domain, middlewares...)
	defer r.leaveGroup()
	fn()
}

// Resource 注册 RESTful 资源路由，并返回可继续裁剪的资源对象。
func (r *Router) Resource(path, controller string) *ResourceRoute {
	resource := &ResourceRoute{
		router: r,
		routes: make(map[string]*Route),
	}
	for _, definition := range resourceDefinitions {
		route := r.Add(definition.method, path+definition.pathSuffix, controller+definition.handler)
		resource.routes[definition.action] = route
	}
	return resource
}

// Match 匹配请求并提取路由参数。
func (r *Router) Match(req *context.Request) (*Route, map[string]string) {
	method := req.Method()
	path := req.Path()

	if route := r.matchStatic(method, path, req.Host()); route != nil {
		return route, nil
	}
	if route := r.matchStatic("*", path, req.Host()); route != nil {
		return route, nil
	}

	// 请求路径只切分一次，供所有动态路由复用。
	host := req.Host()
	requestParts := splitPathParts(path)
	for _, route := range r.dynamicRoutes[method] {
		if !matchDomain(route.domain, host) {
			continue
		}
		if matched, params := route.matchPathParts(path, requestParts); matched {
			return route, params
		}
	}
	for _, route := range r.dynamicRoutes["*"] {
		if !matchDomain(route.domain, host) {
			continue
		}
		if matched, params := route.matchPathParts(path, requestParts); matched {
			return route, params
		}
	}

	// 自动路由：当显式路由匹配失败时，尝试将 URL 路径解析为 Controller@Action
	// 对应 ThinkPHP 的 URL 自动解析规则：/controller/action → Controller@Action
	if r.autoRoute {
		if autoRoute := r.resolveAutoRoute(path); autoRoute != nil {
			return autoRoute, nil
		}
	}

	if r.missRoute != nil {
		return r.missRoute, nil
	}
	return nil, nil
}

// resolveAutoRoute 根据 URL 路径自动解析控制器和动作。
// 支持的 URL 格式：
//   / → Index@index（默认控制器默认动作）
//   /user → User@index（指定控制器默认动作）
//   /user/edit → User@Edit（指定控制器和动作）
//   /admin/user/edit → Admin.User@Edit（多层级控制器）
func (r *Router) resolveAutoRoute(urlPath string) *Route {
	// 去除前后斜杠并分割
	trimmed := strings.Trim(urlPath, "/")
	if trimmed == "" {
		// 根路径：默认控制器 + 默认动作
		return &Route{
			Method:  "*",
			Path:    urlPath,
			Handler: r.defaultController + "@" + ucfirst(r.defaultAction),
			Auto:    true,
		}
	}

	parts := strings.Split(trimmed, "/")
	var controller, action string

	switch len(parts) {
	case 1:
		// /user → User@index
		controller = ucfirst(parts[0])
		action = ucfirst(r.defaultAction)
	default:
		// /user/edit → User@Edit
		// /admin/user/edit → Admin.User@Edit（多层级通过 . 分隔）
		controllerParts := make([]string, 0, len(parts)-1)
		for _, p := range parts[:len(parts)-1] {
			controllerParts = append(controllerParts, ucfirst(p))
		}
		controller = strings.Join(controllerParts, ".")
		action = ucfirst(parts[len(parts)-1])
	}

	return &Route{
		Method:  "*",
		Path:    urlPath,
		Handler: controller + "@" + action,
		Auto:    true,
	}
}

// ucfirst 将字符串首字母转大写。
func ucfirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	if runes[0] >= 'a' && runes[0] <= 'z' {
		runes[0] -= 32
	}
	return string(runes)
}

// GetRoutes 返回已注册的全部路由。
func (r *Router) GetRoutes() []*Route {
	return r.routes
}

// URL 根据命名路由和参数反向生成 URL。
func (r *Router) URL(name string, params map[string]interface{}) (string, error) {
	route, ok := r.namedRoutes[name]
	if !ok || route == nil {
		return "", fmt.Errorf("route %q not found", name)
	}

	consumed := make(map[string]bool)
	parts := splitPathParts(route.Path)
	pathParts := make([]string, 0, len(parts))
	for _, part := range parts {
		if !strings.HasPrefix(part, ":") {
			pathParts = append(pathParts, part)
			continue
		}

		paramName := strings.TrimPrefix(part, ":")
		optional := strings.HasSuffix(paramName, "?")
		paramName = strings.TrimSuffix(paramName, "?")

		value, exists := params[paramName]
		text := ""
		if exists {
			text = fmt.Sprint(value)
		}

		if text == "" {
			if optional {
				continue
			}
			return "", fmt.Errorf("route %q missing required param %q", name, paramName)
		}

		consumed[paramName] = true
		pathParts = append(pathParts, url.PathEscape(text))
	}

	builtPath := "/" + strings.Join(pathParts, "/")
	if builtPath == "//" {
		builtPath = "/"
	}
	if route.ext != "" && !strings.HasSuffix(builtPath, "."+route.ext) {
		builtPath += "." + route.ext
	}

	if len(params) == 0 {
		return builtPath, nil
	}

	queryKeys := make([]string, 0)
	for key, value := range params {
		if consumed[key] || value == nil {
			continue
		}
		queryKeys = append(queryKeys, key)
	}
	if len(queryKeys) == 0 {
		return builtPath, nil
	}

	sort.Strings(queryKeys)
	values := make(url.Values)
	for _, key := range queryKeys {
		values.Set(key, fmt.Sprint(params[key]))
	}
	return builtPath + "?" + values.Encode(), nil
}

// Name 设置路由名称。
func (r *Route) Name(name string) *Route {
	if r.router != nil && r.name != "" && r.router.namedRoutes[r.name] == r {
		delete(r.router.namedRoutes, r.name)
	}
	r.name = name
	if r.router != nil && name != "" {
		r.router.namedRoutes[name] = r
	}
	return r
}

// Pattern 设置参数正则约束。
func (r *Route) Pattern(param, pattern string) *Route {
	r.patterns[param] = pattern
	if compiled, err := regexp.Compile("^" + pattern + "$"); err == nil {
		r.compiledPatterns[param] = compiled
	}
	return r
}

// Domain 设置域名约束。
func (r *Route) Domain(domain string) *Route {
	r.domain = domain
	return r
}

// Ext 设置扩展名约束。
func (r *Route) Ext(ext string) *Route {
	r.ext = ext
	return r
}

// WithoutMiddleware 从当前路由中移除指定中间件。
func (r *Route) WithoutMiddleware(targets ...middleware.Handler) *Route {
	if len(targets) == 0 {
		return r
	}

	filtered := make([]middleware.Handler, 0, len(r.Middleware))
	for _, handler := range r.Middleware {
		if !containsMiddleware(targets, handler) {
			filtered = append(filtered, handler)
		}
	}
	r.Middleware = filtered
	return r
}

// Only 仅保留指定资源动作。
func (r *ResourceRoute) Only(actions ...string) *ResourceRoute {
	allowed := make(map[string]bool, len(actions))
	for _, action := range actions {
		allowed[strings.ToLower(strings.TrimSpace(action))] = true
	}
	for action, route := range r.routes {
		if !allowed[action] {
			r.router.removeRoute(route)
			delete(r.routes, action)
		}
	}
	return r
}

// Except 排除指定资源动作。
func (r *ResourceRoute) Except(actions ...string) *ResourceRoute {
	excluded := make(map[string]bool, len(actions))
	for _, action := range actions {
		excluded[strings.ToLower(strings.TrimSpace(action))] = true
	}
	for action, route := range r.routes {
		if excluded[action] {
			r.router.removeRoute(route)
			delete(r.routes, action)
		}
	}
	return r
}

// Miss 设置 404 兜底路由。
func (r *Router) Miss(handler HandlerFunc) *Route {
	route := &Route{
		Method:           "*",
		Path:             "__miss__",
		Handler:          handler,
		Middleware:       make([]middleware.Handler, 0),
		patterns:         make(map[string]string),
		compiledPatterns: make(map[string]*regexp.Regexp),
		router:           r,
	}
	r.missRoute = route
	return route
}

// Rule 通过 methods 参数批量注册多方法路由。
func (r *Router) Rule(path string, handler HandlerFunc, methods string, middlewares ...middleware.Handler) {
	for _, method := range strings.Split(methods, "|") {
		method = strings.TrimSpace(strings.ToUpper(method))
		if method != "" {
			r.Add(method, path, handler, middlewares...)
		}
	}
}

// Redirect 注册重定向路由。
func (r *Router) Redirect(path, target string, code ...int) {
	statusCode := httpStatusFound
	if len(code) > 0 {
		statusCode = code[0]
	}
	r.Any(path, func(req *context.Request) *context.Response {
		return context.NewResponse().Redirect(target, statusCode)
	})
}

const httpStatusFound = 302

func (r *Router) enterGroup(prefix string, domain string, middlewares ...middleware.Handler) {
	r.groups = append(r.groups, prefix)
	r.groupDomains = append(r.groupDomains, domain)
	r.groupMiddleware = append(r.groupMiddleware, middlewares)
}

func (r *Router) leaveGroup() {
	r.groupMiddleware = r.groupMiddleware[:len(r.groupMiddleware)-1]
	r.groupDomains = r.groupDomains[:len(r.groupDomains)-1]
	r.groups = r.groups[:len(r.groups)-1]
}

func (r *Router) currentGroupDomain() string {
	for index := len(r.groupDomains) - 1; index >= 0; index-- {
		if strings.TrimSpace(r.groupDomains[index]) != "" {
			return strings.TrimSpace(r.groupDomains[index])
		}
	}
	return ""
}

func (r *Router) appendRoute(route *Route) {
	// 预切分动态路由的路径片段，匹配时直接复用，避免每个请求重复 split。
	if !isStaticRoute(route.Path) {
		route.pathParts = splitPathParts(route.Path)
	}
	if isStaticRoute(route.Path) {
		if _, ok := r.staticRoutes[route.Method]; !ok {
			r.staticRoutes[route.Method] = make(map[string]*Route)
		}
		r.staticRoutes[route.Method][route.Path] = route
	} else {
		r.dynamicRoutes[route.Method] = append(r.dynamicRoutes[route.Method], route)
	}
	r.routes = append(r.routes, route)
}

func (r *Router) removeRoute(route *Route) {
	if route == nil {
		return
	}

	for index, current := range r.routes {
		if current == route {
			r.routes = append(r.routes[:index], r.routes[index+1:]...)
			break
		}
	}

	if isStaticRoute(route.Path) {
		if methodRoutes, ok := r.staticRoutes[route.Method]; ok {
			delete(methodRoutes, route.Path)
		}
	} else {
		if routes, ok := r.dynamicRoutes[route.Method]; ok {
			filtered := make([]*Route, 0, len(routes))
			for _, current := range routes {
				if current != route {
					filtered = append(filtered, current)
				}
			}
			r.dynamicRoutes[route.Method] = filtered
		}
	}

	if route.name != "" && r.namedRoutes[route.name] == route {
		delete(r.namedRoutes, route.name)
	}
}

func (r *Router) matchStatic(method string, path string, host string) *Route {
	if methodRoutes, ok := r.staticRoutes[method]; ok {
		if route, ok := methodRoutes[path]; ok && matchDomain(route.domain, host) {
			return route
		}
	}
	return nil
}

// matchPath 匹配请求路径（自行切分），保留单参数签名供测试与外部调用。
func (r *Route) matchPath(requestPath string) (bool, map[string]string) {
	return r.matchPathParts(requestPath, splitPathParts(requestPath))
}

// matchPathParts 用预切分的请求路径片段匹配路由。
// requestParts 由调用方一次性切分后传入，路由自身片段在注册时已预切分。
func (r *Route) matchPathParts(requestPath string, requestParts []string) (bool, map[string]string) {
	if r.Path == requestPath {
		return true, nil
	}
	if isStaticRoute(r.Path) {
		return false, nil
	}

	routeParts := r.pathParts
	if routeParts == nil {
		routeParts = splitPathParts(r.Path)
	}

	params := make(map[string]string)
	if r.matchParts(routeParts, requestParts, 0, 0, params) {
		if len(params) == 0 {
			return true, nil
		}
		return true, params
	}
	return false, nil
}

func (r *Route) matchParts(routeParts []string, requestParts []string, routeIndex int, requestIndex int, params map[string]string) bool {
	if routeIndex == len(routeParts) {
		return requestIndex == len(requestParts)
	}

	part := routeParts[routeIndex]
	if strings.HasPrefix(part, ":") {
		paramName := strings.TrimPrefix(part, ":")
		optional := strings.HasSuffix(paramName, "?")
		paramName = strings.TrimSuffix(paramName, "?")

		if optional {
			if requestIndex < len(requestParts) && r.validateRouteParam(paramName, requestParts[requestIndex]) {
				params[paramName] = requestParts[requestIndex]
				if r.matchParts(routeParts, requestParts, routeIndex+1, requestIndex+1, params) {
					return true
				}
				delete(params, paramName)
			}

			params[paramName] = ""
			if r.matchParts(routeParts, requestParts, routeIndex+1, requestIndex, params) {
				return true
			}
			delete(params, paramName)
			return false
		}

		if requestIndex >= len(requestParts) {
			return false
		}
		if !r.validateRouteParam(paramName, requestParts[requestIndex]) {
			return false
		}
		params[paramName] = requestParts[requestIndex]
		if r.matchParts(routeParts, requestParts, routeIndex+1, requestIndex+1, params) {
			return true
		}
		delete(params, paramName)
		return false
	}

	if requestIndex >= len(requestParts) || part != requestParts[requestIndex] {
		return false
	}
	return r.matchParts(routeParts, requestParts, routeIndex+1, requestIndex+1, params)
}

func (r *Route) validateRouteParam(paramName string, value string) bool {
	if compiled, ok := r.compiledPatterns[paramName]; ok {
		return compiled.MatchString(value)
	}
	return true
}

func isStaticRoute(path string) bool {
	return !strings.Contains(path, ":")
}

func splitPathParts(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return []string{}
	}
	return strings.Split(trimmed, "/")
}

func matchDomain(routeDomain string, host string) bool {
	routeDomain = strings.TrimSpace(routeDomain)
	if routeDomain == "" {
		return true
	}
	return strings.EqualFold(routeDomain, host)
}

func containsMiddleware(targets []middleware.Handler, candidate middleware.Handler) bool {
	for _, target := range targets {
		if middlewareEqual(target, candidate) {
			return true
		}
	}
	return false
}

func middlewareEqual(left middleware.Handler, right middleware.Handler) bool {
	return reflect.ValueOf(left).Pointer() == reflect.ValueOf(right).Pointer()
}
