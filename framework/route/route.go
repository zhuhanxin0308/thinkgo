package route

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"

	"thinkgo/framework/context"
	"thinkgo/framework/middleware"
)

const (
	anyMethod                = "*"
	maxRoutePathLength       = 2048
	maxRouteSegments         = 64
	maxOptionalRouteSegments = 8
	maxRoutePatternLength    = 512
	maxRouteNameLength       = 128
)

var (
	ErrRouterFrozen           = errors.New("路由器已冻结")
	ErrInvalidRoute           = errors.New("路由定义非法")
	ErrDuplicateRoute         = errors.New("路由重复")
	ErrDuplicateRouteName     = errors.New("路由名称重复")
	ErrInvalidRouteHandler    = errors.New("路由处理器非法")
	ErrInvalidRouteMiddleware = errors.New("路由中间件非法")
	ErrInvalidRoutePattern    = errors.New("路由参数约束非法")
	ErrRouteTooComplex        = errors.New("路由结构过于复杂")
	ErrNilRequest             = errors.New("路由匹配请求不能为空")
	ErrInvalidRequestPath     = errors.New("请求路径非法")
	ErrInvalidRouteParameter  = errors.New("路由参数非法")
	ErrInvalidRouteDomain     = errors.New("路由域名非法")
	ErrInvalidResourceAction  = errors.New("资源路由动作非法")
	ErrMethodNotAllowed       = errors.New("请求方法不允许")
)

// HandlerFunc 描述路由处理器，只允许 Controller@Action 字符串或标准处理函数。
type HandlerFunc interface{}

// MethodNotAllowedError 携带稳定排序后的 Allow 方法集合。
type MethodNotAllowedError struct {
	Allowed []string
}

func (e *MethodNotAllowedError) Error() string {
	return fmt.Sprintf("%v，可用方法: %s", ErrMethodNotAllowed, strings.Join(e.Allowed, ", "))
}

func (e *MethodNotAllowedError) Unwrap() error {
	return ErrMethodNotAllowed
}

// Route 是注册完成后的路由对象；字段封闭以防冻结后被绕过修改。
type Route struct {
	router      *Router
	method      string
	path        string
	handler     HandlerFunc
	middlewares []middleware.Handler
	auto        bool
	name        string
	patterns    map[string]string
	compiled    map[string]*regexp.Regexp
	domain      string
	ext         string
	pathParts   []routePart
	order       int
}

type routePart struct {
	literal  string
	param    string
	optional bool
}

// RouteInfo 是不暴露内部可变状态的路由列表快照。
type RouteInfo struct {
	Method          string
	Path            string
	Handler         HandlerFunc
	Name            string
	Domain          string
	Extension       string
	Auto            bool
	MiddlewareCount int
}

// Router 管理注册期路由，并在 Freeze 后提供只读匹配索引。
type Router struct {
	mu sync.RWMutex

	routes        []*Route
	staticRoutes  map[string]map[string][]*Route
	dynamicRoutes map[string][]*Route
	namedRoutes   map[string]*Route
	missRoute     *Route
	frozen        bool
	freezeErr     error

	autoRoute         bool
	defaultController string
	defaultAction     string
}

// Group 是不可变的路由注册作用域，可安全嵌套且不会污染其他协程的注册上下文。
type Group struct {
	router      *Router
	prefix      string
	domain      string
	middlewares []middleware.Handler
}

// ResourceRoute 表示一组可原子裁剪的 RESTful 资源路由。
type ResourceRoute struct {
	router *Router
	routes map[string]*Route
}

type routeDefinition struct {
	method      string
	path        string
	handler     HandlerFunc
	middlewares []middleware.Handler
	domain      string
}

var resourceDefinitions = []struct {
	action     string
	method     string
	pathSuffix string
	handler    string
}{
	{action: "index", method: http.MethodGet, pathSuffix: "", handler: "@Index"},
	{action: "create", method: http.MethodGet, pathSuffix: "/create", handler: "@Create"},
	{action: "save", method: http.MethodPost, pathSuffix: "", handler: "@Save"},
	{action: "read", method: http.MethodGet, pathSuffix: "/:id", handler: "@Read"},
	{action: "edit", method: http.MethodGet, pathSuffix: "/:id/edit", handler: "@Edit"},
	{action: "update", method: http.MethodPut, pathSuffix: "/:id", handler: "@Update"},
	{action: "delete", method: http.MethodDelete, pathSuffix: "/:id", handler: "@Delete"},
}

// NewRouter 创建处于注册状态的路由器。
func NewRouter() *Router {
	return &Router{
		routes:            make([]*Route, 0),
		staticRoutes:      make(map[string]map[string][]*Route),
		dynamicRoutes:     make(map[string][]*Route),
		namedRoutes:       make(map[string]*Route),
		defaultController: "Index",
		defaultAction:     "index",
	}
}

// EnableAutoRoute 设置只读自动路由开关。
func (r *Router) EnableAutoRoute(enable bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return ErrRouterFrozen
	}
	r.autoRoute = enable
	return nil
}

// SetDefaultController 设置自动路由默认控制器。
func (r *Router) SetDefaultController(name string) error {
	if !isValidAutoControllerName(name) {
		return fmt.Errorf("%w: 默认控制器 %q 非法", ErrInvalidRouteHandler, name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return ErrRouterFrozen
	}
	r.defaultController = name
	return nil
}

// SetDefaultAction 设置自动路由默认动作。
func (r *Router) SetDefaultAction(name string) error {
	if !isValidAutoRouteSegment(name) {
		return fmt.Errorf("%w: 默认动作 %q 非法", ErrInvalidRouteHandler, name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return ErrRouterFrozen
	}
	r.defaultAction = name
	return nil
}

func (r *Router) Add(method, path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return r.rootGroup().Add(method, path, handler, handlers...)
}

func (r *Router) Get(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return r.Add(http.MethodGet, path, handler, handlers...)
}

func (r *Router) Post(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return r.Add(http.MethodPost, path, handler, handlers...)
}

func (r *Router) Put(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return r.Add(http.MethodPut, path, handler, handlers...)
}

func (r *Router) Delete(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return r.Add(http.MethodDelete, path, handler, handlers...)
}

func (r *Router) Patch(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return r.Add(http.MethodPatch, path, handler, handlers...)
}

func (r *Router) Options(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return r.Add(http.MethodOptions, path, handler, handlers...)
}

func (r *Router) Head(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return r.Add(http.MethodHead, path, handler, handlers...)
}

func (r *Router) Any(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return r.Add(anyMethod, path, handler, handlers...)
}

// Group 创建前缀隔离的注册作用域。
func (r *Router) Group(prefix string, fn func(*Group) error, handlers ...middleware.Handler) error {
	return r.rootGroup().Group(prefix, fn, handlers...)
}

// Domain 创建域名隔离的注册作用域。
func (r *Router) Domain(domain string, fn func(*Group) error, handlers ...middleware.Handler) error {
	return r.rootGroup().Domain(domain, fn, handlers...)
}

// DomainGroup 创建域名与前缀同时隔离的注册作用域。
func (r *Router) DomainGroup(domain, prefix string, fn func(*Group) error, handlers ...middleware.Handler) error {
	return r.rootGroup().DomainGroup(domain, prefix, fn, handlers...)
}

func (r *Router) Resource(path, controller string) (*ResourceRoute, error) {
	return r.rootGroup().Resource(path, controller)
}

func (r *Router) Rule(path string, handler HandlerFunc, methods string, handlers ...middleware.Handler) ([]*Route, error) {
	return r.rootGroup().Rule(path, handler, methods, handlers...)
}

// Redirect 注册经过状态码和目标校验的重定向路由。
func (r *Router) Redirect(path, target string, codes ...int) (*Route, error) {
	return r.rootGroup().Redirect(path, target, codes...)
}

// Miss 注册唯一的未命中处理器。
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
	r.missRoute = &Route{
		router:      r,
		method:      anyMethod,
		path:        "__miss__",
		handler:     handler,
		middlewares: []middleware.Handler{},
		patterns:    make(map[string]string),
		compiled:    make(map[string]*regexp.Regexp),
	}
	return r.missRoute, nil
}

func (r *Router) rootGroup() *Group {
	return &Group{router: r, middlewares: []middleware.Handler{}}
}

func (g *Group) Add(method, path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	definitions := []routeDefinition{{
		method:      method,
		path:        joinRoutePath(g.prefix, path),
		handler:     handler,
		middlewares: mergeMiddleware(g.middlewares, handlers),
		domain:      g.domain,
	}}
	routes, err := g.router.registerDefinitions(definitions)
	if err != nil {
		return nil, err
	}
	return routes[0], nil
}

func (g *Group) Get(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return g.Add(http.MethodGet, path, handler, handlers...)
}

func (g *Group) Post(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return g.Add(http.MethodPost, path, handler, handlers...)
}

func (g *Group) Put(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return g.Add(http.MethodPut, path, handler, handlers...)
}

func (g *Group) Delete(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return g.Add(http.MethodDelete, path, handler, handlers...)
}

func (g *Group) Patch(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return g.Add(http.MethodPatch, path, handler, handlers...)
}

func (g *Group) Options(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return g.Add(http.MethodOptions, path, handler, handlers...)
}

func (g *Group) Head(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return g.Add(http.MethodHead, path, handler, handlers...)
}

func (g *Group) Any(path string, handler HandlerFunc, handlers ...middleware.Handler) (*Route, error) {
	return g.Add(anyMethod, path, handler, handlers...)
}

func (g *Group) Group(prefix string, fn func(*Group) error, handlers ...middleware.Handler) error {
	return g.scoped("", prefix, fn, handlers...)
}

func (g *Group) Domain(domain string, fn func(*Group) error, handlers ...middleware.Handler) error {
	return g.scoped(domain, "", fn, handlers...)
}

func (g *Group) DomainGroup(domain, prefix string, fn func(*Group) error, handlers ...middleware.Handler) error {
	return g.scoped(domain, prefix, fn, handlers...)
}

func (g *Group) scoped(domain, prefix string, fn func(*Group) error, handlers ...middleware.Handler) error {
	if fn == nil {
		return fmt.Errorf("%w: 分组回调不能为空", ErrInvalidRoute)
	}
	if err := validateMiddleware(handlers); err != nil {
		return err
	}
	nextDomain := g.domain
	if strings.TrimSpace(domain) != "" {
		validated, err := normalizeAndValidateRouteDomain(domain)
		if err != nil {
			return err
		}
		nextDomain = validated
	}
	nextPrefix := joinRoutePath(g.prefix, prefix)
	if nextPrefix == "/" {
		nextPrefix = ""
	}
	if nextPrefix != "" {
		if _, _, err := parseRoutePath(nextPrefix); err != nil {
			return err
		}
	}
	next := &Group{
		router:      g.router,
		prefix:      nextPrefix,
		domain:      nextDomain,
		middlewares: mergeMiddleware(g.middlewares, handlers),
	}
	return fn(next)
}

func (g *Group) Resource(path, controller string) (*ResourceRoute, error) {
	if !isValidAutoControllerName(controller) {
		return nil, fmt.Errorf("%w: 资源控制器 %q 非法", ErrInvalidRouteHandler, controller)
	}
	definitions := make([]routeDefinition, 0, len(resourceDefinitions))
	for _, definition := range resourceDefinitions {
		definitions = append(definitions, routeDefinition{
			method:      definition.method,
			path:        joinRoutePath(g.prefix, path+definition.pathSuffix),
			handler:     controller + definition.handler,
			middlewares: append([]middleware.Handler(nil), g.middlewares...),
			domain:      g.domain,
		})
	}
	routes, err := g.router.registerDefinitions(definitions)
	if err != nil {
		return nil, err
	}
	resource := &ResourceRoute{router: g.router, routes: make(map[string]*Route, len(routes))}
	for index, definition := range resourceDefinitions {
		resource.routes[definition.action] = routes[index]
	}
	return resource, nil
}

func (g *Group) Rule(path string, handler HandlerFunc, methods string, handlers ...middleware.Handler) ([]*Route, error) {
	parts := strings.Split(methods, "|")
	if len(parts) == 0 {
		return nil, fmt.Errorf("%w: methods 不能为空", ErrInvalidRoute)
	}
	definitions := make([]routeDefinition, 0, len(parts))
	for _, method := range parts {
		method = strings.TrimSpace(method)
		if method == "" {
			return nil, fmt.Errorf("%w: methods 包含空项", ErrInvalidRoute)
		}
		definitions = append(definitions, routeDefinition{
			method:      method,
			path:        joinRoutePath(g.prefix, path),
			handler:     handler,
			middlewares: mergeMiddleware(g.middlewares, handlers),
			domain:      g.domain,
		})
	}
	return g.router.registerDefinitions(definitions)
}

func (g *Group) Redirect(path, target string, codes ...int) (*Route, error) {
	if strings.TrimSpace(target) == "" || strings.ContainsAny(target, "\r\n\x00") {
		return nil, fmt.Errorf("%w: 重定向目标非法", ErrInvalidRouteHandler)
	}
	statusCode := http.StatusFound
	if len(codes) > 1 {
		return nil, fmt.Errorf("%w: 重定向状态码只能提供一个", ErrInvalidRoute)
	}
	if len(codes) == 1 {
		statusCode = codes[0]
	}
	if !isRedirectStatus(statusCode) {
		return nil, fmt.Errorf("%w: 重定向状态码 %d 非法", ErrInvalidRoute, statusCode)
	}
	return g.Any(path, func(_ *context.Request) *context.Response {
		return context.NewResponse().Redirect(target, statusCode)
	})
}

// isRedirectStatus 只接受客户端重定向语义明确且仍在通用客户端中使用的状态码。
func isRedirectStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

// Freeze 原子构建全部只读索引，并封闭后续注册和修改。
func (r *Router) Freeze() error {
	r.mu.RLock()
	if r.frozen {
		err := r.freezeErr
		r.mu.RUnlock()
		return err
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return r.freezeErr
	}

	staticRoutes := make(map[string]map[string][]*Route)
	dynamicRoutes := make(map[string][]*Route)
	for _, registered := range r.routes {
		parts, optionalCount, err := parseRoutePath(registered.path)
		if err != nil {
			r.freezeErr = err
			r.frozen = true
			return err
		}
		if optionalCount > maxOptionalRouteSegments {
			r.freezeErr = fmt.Errorf("%w: %s", ErrRouteTooComplex, registered.path)
			r.frozen = true
			return r.freezeErr
		}
		registered.pathParts = parts
		if isStaticRouteParts(parts) {
			if _, ok := staticRoutes[registered.method]; !ok {
				staticRoutes[registered.method] = make(map[string][]*Route)
			}
			key := pathPartsKey(routePartsWithExtension(parts, registered.ext))
			staticRoutes[registered.method][key] = append(staticRoutes[registered.method][key], registered)
			continue
		}
		dynamicRoutes[registered.method] = append(dynamicRoutes[registered.method], registered)
	}
	for method := range dynamicRoutes {
		sort.SliceStable(dynamicRoutes[method], func(left, right int) bool {
			return routeSpecificity(dynamicRoutes[method][left]) > routeSpecificity(dynamicRoutes[method][right])
		})
	}
	r.staticRoutes = staticRoutes
	r.dynamicRoutes = dynamicRoutes
	r.freezeErr = nil
	r.frozen = true
	return nil
}

// Routes 返回稳定的路由元数据快照。
func (r *Router) Routes() ([]RouteInfo, error) {
	if err := r.Freeze(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]RouteInfo, 0, len(r.routes))
	for _, registered := range r.routes {
		result = append(result, RouteInfo{
			Method:          registered.method,
			Path:            registered.path,
			Handler:         registered.handler,
			Name:            registered.name,
			Domain:          registered.domain,
			Extension:       registered.ext,
			Auto:            registered.auto,
			MiddlewareCount: len(registered.middlewares),
		})
	}
	return result, nil
}

func (r *Router) registerDefinitions(definitions []routeDefinition) ([]*Route, error) {
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
		if err = validateMiddleware(definition.middlewares); err != nil {
			return nil, err
		}
		domain, err := normalizeAndValidateRouteDomain(definition.domain)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, &Route{
			router:      r,
			method:      method,
			path:        path,
			handler:     definition.handler,
			middlewares: append([]middleware.Handler(nil), definition.middlewares...),
			patterns:    make(map[string]string),
			compiled:    make(map[string]*regexp.Regexp),
			domain:      domain,
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
	for _, registered := range prepared {
		registered.order = len(r.routes)
		r.routes = append(r.routes, registered)
	}
	return prepared, nil
}

func (r *Router) findDuplicateLocked(candidate, ignored *Route, candidates []*Route) *Route {
	for _, existing := range r.routes {
		if existing != ignored && sameRouteIdentity(existing, candidate) {
			return existing
		}
	}
	for _, existing := range candidates {
		if existing != candidate && existing != ignored && sameRouteIdentity(existing, candidate) {
			return existing
		}
	}
	return nil
}

func sameRouteIdentity(left, right *Route) bool {
	return left.method == right.method && left.path == right.path && left.domain == right.domain && left.ext == right.ext
}

// mutableRouter 返回路由所属的可配置路由器，零值路由统一以错误方式安全失败。
func (r *Route) mutableRouter() (*Router, error) {
	if r == nil || r.router == nil {
		return nil, ErrInvalidRoute
	}
	return r.router, nil
}

// WithName 设置唯一的路由名称。
func (r *Route) WithName(name string) error {
	router, err := r.mutableRouter()
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if !isValidRouteName(name) {
		return fmt.Errorf("%w: %q", ErrInvalidRoute, name)
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.frozen {
		return ErrRouterFrozen
	}
	if existing := router.namedRoutes[name]; existing != nil && existing != r {
		return fmt.Errorf("%w: %q", ErrDuplicateRouteName, name)
	}
	if r.name != "" {
		delete(router.namedRoutes, r.name)
	}
	r.name = name
	router.namedRoutes[name] = r
	return nil
}

// WithPattern 设置已声明参数的正则约束。
func (r *Route) WithPattern(param, pattern string) error {
	router, err := r.mutableRouter()
	if err != nil {
		return err
	}
	param = strings.TrimSpace(param)
	if !routeHasParam(r.path, param) {
		return fmt.Errorf("%w: 路径 %s 不包含参数 %q", ErrInvalidRoutePattern, r.path, param)
	}
	if pattern == "" || len(pattern) > maxRoutePatternLength || strings.Contains(pattern, "(?P<") || strings.Contains(pattern, "(?<") {
		return fmt.Errorf("%w: 参数 %q 的约束非法", ErrInvalidRoutePattern, param)
	}
	compiled, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return fmt.Errorf("%w: 参数 %q: %v", ErrInvalidRoutePattern, param, err)
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.frozen {
		return ErrRouterFrozen
	}
	r.patterns[param] = pattern
	r.compiled[param] = compiled
	return nil
}

// WithDomain 设置精确域名约束。
func (r *Route) WithDomain(domain string) error {
	router, routeErr := r.mutableRouter()
	if routeErr != nil {
		return routeErr
	}
	normalized, err := normalizeAndValidateRouteDomain(domain)
	if err != nil {
		return err
	}
	if normalized == "" {
		return fmt.Errorf("%w: 单路由域名不能为空", ErrInvalidRouteDomain)
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.frozen {
		return ErrRouterFrozen
	}
	candidate := *r
	candidate.domain = normalized
	if router.findDuplicateLocked(&candidate, r, nil) != nil {
		return fmt.Errorf("%w: %s %s domain=%q", ErrDuplicateRoute, r.method, r.path, normalized)
	}
	r.domain = normalized
	return nil
}

// WithExtension 设置参与匹配和 URL 生成的扩展名约束。
func (r *Route) WithExtension(extension string) error {
	router, routeErr := r.mutableRouter()
	if routeErr != nil {
		return routeErr
	}
	normalized, err := normalizeRouteExtension(extension)
	if err != nil {
		return err
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.frozen {
		return ErrRouterFrozen
	}
	candidate := *r
	candidate.ext = normalized
	if router.findDuplicateLocked(&candidate, r, nil) != nil {
		return fmt.Errorf("%w: %s %s extension=%q", ErrDuplicateRoute, r.method, r.path, normalized)
	}
	r.ext = normalized
	return nil
}

// WithoutMiddleware 仅在目标函数指针唯一时移除，歧义时失败关闭。
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
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.frozen {
		return ErrRouterFrozen
	}
	working := append([]middleware.Handler(nil), r.middlewares...)
	for _, target := range targets {
		pointer := reflect.ValueOf(target).Pointer()
		matchedIndex := -1
		for index, candidate := range working {
			if reflect.ValueOf(candidate).Pointer() != pointer {
				continue
			}
			if matchedIndex >= 0 {
				return fmt.Errorf("%w: 中间件函数身份存在歧义", ErrInvalidRouteMiddleware)
			}
			matchedIndex = index
		}
		if matchedIndex < 0 {
			return fmt.Errorf("%w: 待移除中间件不存在", ErrInvalidRouteMiddleware)
		}
		working = append(working[:matchedIndex], working[matchedIndex+1:]...)
	}
	r.middlewares = working
	return nil
}

func (r *Route) Method() string {
	if r == nil {
		return ""
	}
	if r.router != nil {
		r.router.mu.RLock()
		defer r.router.mu.RUnlock()
	}
	return r.method
}

func (r *Route) Path() string {
	if r == nil {
		return ""
	}
	if r.router != nil {
		r.router.mu.RLock()
		defer r.router.mu.RUnlock()
	}
	return r.path
}

func (r *Route) Handler() HandlerFunc {
	if r == nil {
		return nil
	}
	if r.router != nil {
		r.router.mu.RLock()
		defer r.router.mu.RUnlock()
	}
	return r.handler
}

func (r *Route) IsAuto() bool {
	if r == nil {
		return false
	}
	if r.router != nil {
		r.router.mu.RLock()
		defer r.router.mu.RUnlock()
	}
	return r.auto
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
	return append([]middleware.Handler(nil), r.middlewares...)
}

func (r *ResourceRoute) Only(actions ...string) error {
	if len(actions) == 0 {
		return fmt.Errorf("%w: Only 至少需要一个动作", ErrInvalidResourceAction)
	}
	selected, err := validateResourceActions(actions)
	if err != nil {
		return err
	}
	return r.filter(func(action string) bool { return selected[action] })
}

func (r *ResourceRoute) Except(actions ...string) error {
	selected, err := validateResourceActions(actions)
	if err != nil {
		return err
	}
	return r.filter(func(action string) bool { return !selected[action] })
}

func (r *ResourceRoute) filter(keep func(string) bool) error {
	if r == nil || r.router == nil {
		return ErrInvalidRoute
	}
	r.router.mu.Lock()
	defer r.router.mu.Unlock()
	if r.router.frozen {
		return ErrRouterFrozen
	}
	for action, registered := range r.routes {
		if keep(action) {
			continue
		}
		r.router.removeRouteLocked(registered)
		delete(r.routes, action)
	}
	return nil
}

func (r *Router) removeRouteLocked(target *Route) {
	for index, registered := range r.routes {
		if registered == target {
			r.routes = append(r.routes[:index], r.routes[index+1:]...)
			break
		}
	}
	if target.name != "" && r.namedRoutes[target.name] == target {
		delete(r.namedRoutes, target.name)
	}
}

func validateResourceActions(actions []string) (map[string]bool, error) {
	valid := make(map[string]bool, len(resourceDefinitions))
	for _, definition := range resourceDefinitions {
		valid[definition.action] = true
	}
	selected := make(map[string]bool, len(actions))
	for _, action := range actions {
		action = strings.ToLower(strings.TrimSpace(action))
		if !valid[action] {
			return nil, fmt.Errorf("%w: %q", ErrInvalidResourceAction, action)
		}
		selected[action] = true
	}
	return selected, nil
}

func validateRouteHandler(handler HandlerFunc) error {
	switch typed := handler.(type) {
	case string:
		parts := strings.Split(typed, "@")
		if len(parts) != 2 || !isValidAutoControllerName(parts[0]) || !isValidAutoRouteSegment(parts[1]) {
			return fmt.Errorf("%w: %q", ErrInvalidRouteHandler, typed)
		}
	case func(*context.Request) *context.Response:
		if typed == nil {
			return ErrInvalidRouteHandler
		}
	default:
		return fmt.Errorf("%w: 类型 %T", ErrInvalidRouteHandler, handler)
	}
	return nil
}

func validateMiddleware(handlers []middleware.Handler) error {
	for index, handler := range handlers {
		if handler == nil {
			return fmt.Errorf("%w: 第 %d 项为空", ErrInvalidRouteMiddleware, index+1)
		}
	}
	return nil
}

func mergeMiddleware(parent, current []middleware.Handler) []middleware.Handler {
	merged := make([]middleware.Handler, 0, len(parent)+len(current))
	merged = append(merged, parent...)
	merged = append(merged, current...)
	return merged
}

func normalizeRouteMethod(method string) (string, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == anyMethod {
		return method, nil
	}
	if method == "" {
		return "", fmt.Errorf("%w: HTTP 方法为空", ErrInvalidRoute)
	}
	for _, char := range method {
		if !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", char) {
			return "", fmt.Errorf("%w: HTTP 方法 %q 非法", ErrInvalidRoute, method)
		}
	}
	return method, nil
}

func isValidRouteName(name string) bool {
	if name == "" || len(name) > maxRouteNameLength {
		return false
	}
	for index, char := range name {
		letter := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
		digit := char >= '0' && char <= '9'
		if index == 0 && !letter && char != '_' {
			return false
		}
		if index > 0 && !letter && !digit && !strings.ContainsRune("._-", char) {
			return false
		}
	}
	return true
}

func routeHasParam(path, name string) bool {
	if name == "" {
		return false
	}
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		if strings.TrimSuffix(strings.TrimPrefix(part, ":"), "?") == name && strings.HasPrefix(part, ":") {
			return true
		}
	}
	return false
}
