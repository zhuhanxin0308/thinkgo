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

	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
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
)

// HandlerFunc 描述路由处理器，支持 controller/action、ThinkPHP 风格回调和 net/http 处理器。
type HandlerFunc interface{}

// Route 是注册完成后的路由对象；字段封闭以防冻结后被绕过修改。
type Route struct {
	router             *Router
	method             string
	path               string
	handler            HandlerFunc
	middlewares        []middleware.Handler
	middlewarePipeline *middleware.Pipeline
	auto               bool
	name               string
	patterns           map[string]string
	compiled           map[string]*regexp.Regexp
	domain             string
	ext                string
	extensionRequired  bool
	jsonContract       bool
	pathParts          []routePart
	trailingSlash      bool
	controllerLayer    string
	order              int
}

type routePart struct {
	literal  string
	param    string
	optional bool
}

// ParameterNames 返回路由变量的声明顺序，供控制器动作按 ThinkPHP 的
// 参数绑定规则稳定注入标量值。返回值是副本，调用方不能修改路由定义。
func (r *Route) ParameterNames() []string {
	if r == nil {
		return nil
	}
	parts := r.pathParts
	if len(parts) == 0 && r.path != "" {
		parsed, _, err := parseRoutePath(r.path)
		if err == nil {
			parts = parsed
		}
	}
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.param != "" {
			names = append(names, part.param)
		}
	}
	return names
}

// RouteInfo 是不暴露内部可变状态的路由列表快照。
type RouteInfo struct {
	Method            string
	Path              string
	Handler           HandlerFunc
	Name              string
	Domain            string
	Extension         string
	ExtensionRequired bool
	Auto              bool
	MiddlewareCount   int
}

// Router 管理注册期路由，并在 Freeze 后提供只读匹配索引。
type Router struct {
	mu sync.RWMutex

	routes        []*Route
	staticRoutes  map[string]map[string][]*Route
	dynamicRoutes map[string][]*Route
	dynamicIndex  *routeIndex
	namedRoutes   map[string]*Route
	cachedNames   map[string]*Route
	missRoute     *Route
	missRoutes    map[string]*Route
	frozen        bool
	freezeErr     error

	autoRoute              bool
	defaultController      string
	defaultAction          string
	caseSensitive          bool
	completeMatch          bool
	removeSlash            bool
	defaultExtension       string
	defaultPattern         string
	compiledDefaultPattern *regexp.Regexp
	controllerLayer        string
}

// Group 是不可变的路由注册作用域，可安全嵌套且不会污染其他协程的注册上下文。
type Group struct {
	router      *Router
	prefix      string
	domain      string
	middlewares []middleware.Handler
}

type routeDefinition struct {
	method      string
	path        string
	handler     HandlerFunc
	middlewares []middleware.Handler
	domain      string
}

// NewRouter 创建处于注册状态的路由器。
func NewRouter() *Router {
	return &Router{
		routes:                 make([]*Route, 0),
		staticRoutes:           make(map[string]map[string][]*Route),
		dynamicRoutes:          make(map[string][]*Route),
		namedRoutes:            make(map[string]*Route),
		missRoutes:             make(map[string]*Route),
		autoRoute:              true,
		defaultController:      "Index",
		defaultAction:          "index",
		caseSensitive:          false,
		completeMatch:          false,
		removeSlash:            false,
		defaultExtension:       "html",
		defaultPattern:         `[\w\.]+`,
		compiledDefaultPattern: regexp.MustCompile(`^(?:[\w\.]+)$`),
		controllerLayer:        "controller",
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

// SetCaseSensitive 设置静态路由和字面量段是否区分大小写。
func (r *Router) SetCaseSensitive(caseSensitive bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return ErrRouterFrozen
	}
	r.caseSensitive = caseSensitive
	return nil
}

// SetCompleteMatch 设置路由是否必须完整消费请求路径。
func (r *Router) SetCompleteMatch(completeMatch bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return ErrRouterFrozen
	}
	r.completeMatch = completeMatch
	return nil
}

// SetRemoveSlash 设置是否忽略请求和路由末尾的斜杠差异。
func (r *Router) SetRemoveSlash(removeSlash bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return ErrRouterFrozen
	}
	r.removeSlash = removeSlash
	return nil
}

// SetDefaultExtension 设置全局 URL 后缀；全局后缀可选，显式 WithExtension 仍为强制约束。
func (r *Router) SetDefaultExtension(extension string) error {
	normalized, err := normalizeOptionalRouteExtension(extension)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return ErrRouterFrozen
	}
	r.defaultExtension = normalized
	return nil
}

// SetDefaultPattern 设置没有显式 Pattern 的路由变量约束。
func (r *Router) SetDefaultPattern(pattern string) error {
	if pattern == "" || len(pattern) > maxRoutePatternLength || strings.Contains(pattern, "(?P<") || strings.Contains(pattern, "(?<") {
		return fmt.Errorf("%w: 默认路由变量约束非法", ErrInvalidRoutePattern)
	}
	compiled, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return fmt.Errorf("%w: 默认路由变量约束: %v", ErrInvalidRoutePattern, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return ErrRouterFrozen
	}
	r.defaultPattern = pattern
	r.compiledDefaultPattern = compiled
	return nil
}

// SetControllerLayer 设置自动路由的控制器层查找前缀。
func (r *Router) SetControllerLayer(layer string) error {
	layer = strings.TrimSpace(layer)
	if layer != "" && !isValidAutoControllerName(layer) {
		return fmt.Errorf("%w: 控制器层 %q 非法", ErrInvalidRouteHandler, layer)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return ErrRouterFrozen
	}
	r.controllerLayer = layer
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

func (r *Router) Rule(path string, handler HandlerFunc, methods string, handlers ...middleware.Handler) ([]*Route, error) {
	return r.rootGroup().Rule(path, handler, methods, handlers...)
}

// Redirect 注册经过状态码和目标校验的重定向路由。
func (r *Router) Redirect(path, target string, codes ...int) (*Route, error) {
	return r.rootGroup().Redirect(path, target, codes...)
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
		if registered.ext == "" && r.defaultExtension != "" && !registered.jsonContract {
			registered.ext = r.defaultExtension
		}
		if isStaticRouteParts(parts) {
			if _, ok := staticRoutes[registered.method]; !ok {
				staticRoutes[registered.method] = make(map[string][]*Route)
			}
			key := r.pathPartsKey(routePartsWithExtension(parts, registered.ext))
			staticRoutes[registered.method][key] = append(staticRoutes[registered.method][key], registered)
			if !registered.extensionRequired && registered.ext != "" {
				baseKey := r.pathPartsKey(routePartsWithExtension(parts, ""))
				staticRoutes[registered.method][baseKey] = append(staticRoutes[registered.method][baseKey], registered)
			}
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
	r.dynamicIndex = buildRouteIndex(dynamicRoutes)
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
			Method:            registered.method,
			Path:              registered.path,
			Handler:           registered.handler,
			Name:              registered.name,
			Domain:            registered.domain,
			Extension:         registered.ext,
			ExtensionRequired: registered.extensionRequired,
			Auto:              registered.auto,
			MiddlewareCount:   len(registered.middlewares),
		})
	}
	return result, nil
}

func (r *Router) registerDefinitions(definitions []routeDefinition) ([]*Route, error) {
	return r.registerDefinitionsWithCommit(definitions, nil)
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
	r.extensionRequired = true
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

// ControllerLayer 返回自动路由使用的控制器层前缀。
func (r *Route) ControllerLayer() string {
	if r == nil {
		return ""
	}
	if r.router != nil {
		r.router.mu.RLock()
		defer r.router.mu.RUnlock()
	}
	return r.controllerLayer
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

func validateRouteHandler(handler HandlerFunc) error {
	switch typed := handler.(type) {
	case *JSONHandler:
		if typed == nil || typed.callback == nil {
			return ErrInvalidRouteHandler
		}
	case string:
		if !isValidControllerHandler(typed) {
			return fmt.Errorf("%w: %q", ErrInvalidRouteHandler, typed)
		}
	case func(*context.Request) *context.Response:
		if typed == nil {
			return ErrInvalidRouteHandler
		}
	case http.Handler:
		if typed == nil || isNilRouteHandler(typed) {
			return ErrInvalidRouteHandler
		}
	case func(http.ResponseWriter, *http.Request):
		if typed == nil {
			return ErrInvalidRouteHandler
		}
	default:
		value := reflect.ValueOf(handler)
		if !value.IsValid() || value.Kind() != reflect.Func || value.IsNil() {
			return fmt.Errorf("%w: 类型 %T", ErrInvalidRouteHandler, handler)
		}
		if err := validateRouteCallbackSignature(value.Type()); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRouteHandler, err)
		}
	}
	return nil
}

// validateRouteCallbackSignature 对应 ThinkPHP Callback 调度的启动期校验。
// 具体的容器依赖解析和路由变量转换由 HTTP 内核在请求作用域内完成。
func validateRouteCallbackSignature(callbackType reflect.Type) error {
	_, err := binding.CompileCall(callbackType, false)
	return err
}

func isValidControllerHandler(handler string) bool {
	if strings.Contains(handler, "@") {
		parts := strings.Split(handler, "@")
		return len(parts) == 2 && isValidAutoControllerName(parts[0]) && isValidAutoRouteSegment(parts[1])
	}
	parts := strings.Split(strings.Trim(handler, "/"), "/")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if !isValidAutoRouteSegment(part) {
			return false
		}
	}
	return true
}

func isNilRouteHandler(handler http.Handler) bool {
	value := reflect.ValueOf(handler)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
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
