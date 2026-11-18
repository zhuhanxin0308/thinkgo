package framework

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	frameworkContext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/debug"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	frameworkRoute "github.com/zhuhanxin0308/thinkgo/v3/route"
)

// Route 是面向业务代码的 ThinkPHP 风格路由门面。
//
// 底层 Router 保留显式 error，供框架内部安全装配；门面把路由定义错误作为
// 启动期编程错误抛出，使 route/app.go 不必为每条规则重复编写错误分支。
type Route struct {
	app        *App
	router     *frameworkRoute.Router
	scopeMu    sync.RWMutex
	scopeStack []*RuleGroup
}

// RuleItem 对应 ThinkPHP 的 think\route\RuleItem，并提供链式规则配置。
type RuleItem struct {
	rules []*frameworkRoute.Route
}

// RuleGroup 对应 ThinkPHP 的 think\route\RuleGroup。
type RuleGroup struct {
	route     *Route
	group     *frameworkRoute.Group
	rulesMu   *sync.Mutex
	rules     *[]*frameworkRoute.Route
	completed bool
}

// Resource 对应 ThinkPHP 的资源路由注册对象。
type Resource struct {
	resource *frameworkRoute.ResourceRoute
}

func newRouteFacade(app *App, router *frameworkRoute.Router) *Route {
	if router == nil {
		return nil
	}
	return &Route{app: app, router: router}
}

func newRuleItem(rules ...*frameworkRoute.Route) *RuleItem {
	return &RuleItem{rules: append([]*frameworkRoute.Route(nil), rules...)}
}

func mustRouteRule(rule *frameworkRoute.Route, err error) *RuleItem {
	if err != nil {
		panic(err)
	}
	return newRuleItem(rule)
}

func mustRouteRules(rules []*frameworkRoute.Route, err error) *RuleItem {
	if err != nil {
		panic(err)
	}
	return newRuleItem(rules...)
}

func mustRouteConfiguration(err error) {
	if err != nil {
		panic(err)
	}
}

func newScopedRuleGroup(facade *Route, group *frameworkRoute.Group) *RuleGroup {
	collected := make([]*frameworkRoute.Route, 0)
	return &RuleGroup{
		route:   facade,
		group:   group,
		rulesMu: &sync.Mutex{},
		rules:   &collected,
	}
}

func (g *RuleGroup) appendCollectedRoutes(rules ...*frameworkRoute.Route) {
	if g == nil || g.rules == nil || len(rules) == 0 {
		return
	}
	g.rulesMu.Lock()
	*g.rules = append(*g.rules, rules...)
	g.rulesMu.Unlock()
}

func (g *RuleGroup) snapshotRules() []*frameworkRoute.Route {
	if g == nil || g.rules == nil {
		return nil
	}
	g.rulesMu.Lock()
	defer g.rulesMu.Unlock()
	return append([]*frameworkRoute.Route(nil), (*g.rules)...)
}

func (g *RuleGroup) withUpdatedGroup(group *frameworkRoute.Group) *RuleGroup {
	return &RuleGroup{
		route:     g.route,
		group:     group,
		rulesMu:   g.rulesMu,
		rules:     g.rules,
		completed: g.completed,
	}
}

func (g *RuleGroup) recordRoutes(rules ...*frameworkRoute.Route) {
	if len(rules) == 0 {
		return
	}
	if g != nil && g.completed {
		g.appendCollectedRoutes(rules...)
	}
	if g != nil && g.route != nil {
		g.route.recordRoutes(rules...)
	}
}

// Add 注册指定 HTTP 方法的路由。
func (r *Route) Add(method, rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	if r == nil || r.router == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	var (
		registered *frameworkRoute.Route
		err        error
	)
	if group := r.currentScope(); group != nil {
		registered, err = group.Add(method, rule, handler, handlers...)
	} else {
		registered, err = r.router.Add(method, rule, handler, handlers...)
	}
	item := mustRouteRule(registered, err)
	r.recordRoutes(registered)
	return item
}

// Rule 注册一个或多个 HTTP 方法；未指定方法时与 ThinkPHP 一致匹配任意方法。
func (r *Route) Rule(rule string, handler frameworkRoute.HandlerFunc, methods ...string) *RuleItem {
	if r == nil || r.router == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	method := "*"
	if len(methods) > 1 {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	if len(methods) == 1 {
		method = strings.TrimSpace(methods[0])
	}
	var (
		registered []*frameworkRoute.Route
		err        error
	)
	if group := r.currentScope(); group != nil {
		registered, err = group.Rule(rule, handler, method)
	} else {
		registered, err = r.router.Rule(rule, handler, method)
	}
	item := mustRouteRules(registered, err)
	r.recordRoutes(registered...)
	return item
}

// Any 注册任意方法路由。
func (r *Route) Any(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return r.Add("*", rule, handler, handlers...)
}

// Get 注册 GET 路由。
func (r *Route) Get(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return r.Add(http.MethodGet, rule, handler, handlers...)
}

// Post 注册 POST 路由。
func (r *Route) Post(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return r.Add(http.MethodPost, rule, handler, handlers...)
}

// Put 注册 PUT 路由。
func (r *Route) Put(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return r.Add(http.MethodPut, rule, handler, handlers...)
}

// Delete 注册 DELETE 路由。
func (r *Route) Delete(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return r.Add(http.MethodDelete, rule, handler, handlers...)
}

// Patch 注册 PATCH 路由。
func (r *Route) Patch(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return r.Add(http.MethodPatch, rule, handler, handlers...)
}

// Head 注册 HEAD 路由。
func (r *Route) Head(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return r.Add(http.MethodHead, rule, handler, handlers...)
}

// Options 注册 OPTIONS 路由。
func (r *Route) Options(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return r.Add(http.MethodOptions, rule, handler, handlers...)
}

// View 注册 GET 视图路由，对应 ThinkPHP Route::view。
func (r *Route) View(rule string, arguments ...interface{}) *RuleItem {
	if r == nil || r.router == nil || r.app == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	template, variables := parseRouteViewArguments(arguments)
	return r.Get(rule, func(request *frameworkContext.Request) *frameworkContext.Response {
		manager := r.app.View()
		if manager == nil {
			panic(fmt.Errorf("视图路由 %q 缺少视图服务", rule))
		}
		content, err := manager.FetchWithDebug(debug.FromRequest(request), template, cloneRouteViewVariables(variables))
		if err != nil {
			panic(fmt.Errorf("渲染视图路由 %q 失败: %w", rule, err))
		}
		return frameworkContext.NewResponse().HTML(content)
	})
}

func parseRouteViewArguments(arguments []interface{}) (string, map[string]interface{}) {
	if len(arguments) > 2 {
		panic(fmt.Errorf("%w: View 最多接收 template 和 vars 两个可选参数", frameworkRoute.ErrInvalidRoute))
	}
	template := ""
	variables := map[string]interface{}{}
	if len(arguments) >= 1 {
		configured, ok := arguments[0].(string)
		if !ok {
			panic(fmt.Errorf("%w: View template 必须是 string，实际为 %T", frameworkRoute.ErrInvalidRoute, arguments[0]))
		}
		template = configured
	}
	if len(arguments) == 2 {
		configured, ok := arguments[1].(map[string]interface{})
		if !ok {
			panic(fmt.Errorf("%w: View vars 必须是 map[string]interface{}，实际为 %T", frameworkRoute.ErrInvalidRoute, arguments[1]))
		}
		variables = configured
	}
	return template, cloneRouteViewVariables(variables)
}

func cloneRouteViewVariables(source map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

// Redirect 注册重定向路由，默认状态码与 ThinkPHP 一致为 301。
func (r *Route) Redirect(rule, target string, status ...int) *RuleItem {
	if r == nil || r.router == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	if len(status) == 0 {
		status = []int{301}
	}
	var (
		registered *frameworkRoute.Route
		err        error
	)
	if group := r.currentScope(); group != nil {
		registered, err = group.Redirect(rule, target, status...)
	} else {
		registered, err = r.router.Redirect(rule, target, status...)
	}
	item := mustRouteRule(registered, err)
	r.recordRoutes(registered)
	return item
}

// Miss 注册未匹配路由处理器，并支持 ThinkPHP 的 method 可选参数与当前分组作用域。
func (r *Route) Miss(handler frameworkRoute.HandlerFunc, methods ...string) *RuleItem {
	if r == nil || r.router == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	method := "*"
	if len(methods) > 1 {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	if len(methods) == 1 {
		method = methods[0]
	}
	var (
		registered *frameworkRoute.Route
		err        error
	)
	if group := r.currentScope(); group != nil {
		registered, err = group.SetMiss(method, handler)
	} else {
		registered, err = r.router.SetMiss(method, handler)
	}
	item := mustRouteRule(registered, err)
	r.recordRoutes(registered)
	return item
}

// Resource 注册完整 RESTful 资源路由。
func (r *Route) Resource(rule, controller string) *Resource {
	if r == nil || r.router == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	var resource *frameworkRoute.ResourceRoute
	var err error
	if group := r.currentScope(); group != nil {
		resource, err = group.Resource(rule, controller)
	} else {
		resource, err = r.router.Resource(rule, controller)
	}
	mustRouteConfiguration(err)
	r.recordRoutes(resource.Routes()...)
	return &Resource{resource: resource}
}

// Middleware 创建携带匿名中间件的注册分组，支持链式前置分组定义。
func (r *Route) Middleware(handlers ...middleware.Handler) *RuleGroup {
	return r.baseRuleGroup().Middleware(handlers...)
}

// NamedMiddleware 创建携带命名中间件的注册分组，支持链式前置分组定义。
func (r *Route) NamedMiddleware(name string, handler middleware.Handler) *RuleGroup {
	return r.baseRuleGroup().NamedMiddleware(name, handler)
}

// Ext 创建携带扩展名约束的注册分组，支持链式前置分组定义。
func (r *Route) Ext(extension string) *RuleGroup {
	return r.baseRuleGroup().Ext(extension)
}

// Pattern 创建携带路由变量规则的注册分组，支持链式前置分组定义。
func (r *Route) Pattern(patterns map[string]string) *RuleGroup {
	return r.baseRuleGroup().Pattern(patterns)
}

// Where 创建携带路由变量规则的注册分组，对应 ThinkPHP Route::where。
func (r *Route) Where(patterns map[string]string) *RuleGroup {
	return r.Pattern(patterns)
}

func (r *Route) baseRuleGroup() *RuleGroup {
	if r == nil || r.router == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	group := r.currentScope()
	if group == nil {
		group = r.router.RootGroup()
	}
	return &RuleGroup{route: r, group: group}
}

func parseRouteGroupArguments(arguments []interface{}) (interface{}, []middleware.Handler) {
	callback := arguments[0]
	if callback == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	handlers := make([]middleware.Handler, 0, len(arguments)-1)
	for _, raw := range arguments[1:] {
		if raw == nil {
			panic(frameworkRoute.ErrInvalidRouteMiddleware)
		}
		handler, ok := raw.(middleware.Handler)
		if !ok {
			if fn, fnOK := raw.(func(*frameworkContext.Request, func(*frameworkContext.Request) *frameworkContext.Response) *frameworkContext.Response); fnOK {
				handler = middleware.Handler(fn)
			} else {
				panic(fmt.Errorf("%w: 分组中间件类型 %T 非法", frameworkRoute.ErrInvalidRouteMiddleware, raw))
			}
		}
		handlers = append(handlers, handler)
	}
	return callback, handlers
}

// Group 创建路由前缀分组。回调既支持 ThinkPHP 原生的 func() 写法，也保留
// 显式接收 RuleGroup 的 Go 强类型写法。
func (r *Route) Group(prefix string, callback interface{}, handlers ...middleware.Handler) *RuleGroup {
	if r == nil || r.router == nil || callback == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	var result *RuleGroup
	register := func(group *frameworkRoute.Group) error {
		result = newScopedRuleGroup(r, group)
		r.withScope(result, func() { invokeRouteGroupCallback(callback, result) })
		result.completed = true
		return nil
	}
	var err error
	if group := r.currentScope(); group != nil {
		err = group.Group(prefix, register, handlers...)
	} else {
		err = r.router.Group(prefix, register, handlers...)
	}
	mustRouteConfiguration(err)
	return result
}

// Domain 创建域名路由分组；仅传入域名时返回链式前置分组。
func (r *Route) Domain(domain string, arguments ...interface{}) *RuleGroup {
	if r == nil || r.router == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	if len(arguments) == 0 {
		return r.baseRuleGroup().Domain(domain)
	}
	callback, handlers := parseRouteGroupArguments(arguments)
	var result *RuleGroup
	register := func(group *frameworkRoute.Group) error {
		result = newScopedRuleGroup(r, group)
		r.withScope(result, func() { invokeRouteGroupCallback(callback, result) })
		result.completed = true
		return nil
	}
	var err error
	if group := r.currentScope(); group != nil {
		err = group.Domain(domain, register, handlers...)
	} else {
		err = r.router.Domain(domain, register, handlers...)
	}
	mustRouteConfiguration(err)
	return result
}

// DomainGroup 创建同时包含域名和路径前缀的分组。
func (r *Route) DomainGroup(domain, prefix string, callback interface{}, handlers ...middleware.Handler) *RuleGroup {
	if r == nil || r.router == nil || callback == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	var result *RuleGroup
	register := func(group *frameworkRoute.Group) error {
		result = newScopedRuleGroup(r, group)
		r.withScope(result, func() { invokeRouteGroupCallback(callback, result) })
		result.completed = true
		return nil
	}
	var err error
	if group := r.currentScope(); group != nil {
		err = group.DomainGroup(domain, prefix, register, handlers...)
	} else {
		err = r.router.DomainGroup(domain, prefix, register, handlers...)
	}
	mustRouteConfiguration(err)
	return result
}

func (r *Route) currentScope() *frameworkRoute.Group {
	if r == nil {
		return nil
	}
	r.scopeMu.RLock()
	defer r.scopeMu.RUnlock()
	if len(r.scopeStack) == 0 {
		return nil
	}
	return r.scopeStack[len(r.scopeStack)-1].group
}

func (r *Route) recordRoutes(rules ...*frameworkRoute.Route) {
	if r == nil || len(rules) == 0 {
		return
	}
	r.scopeMu.RLock()
	stack := append([]*RuleGroup(nil), r.scopeStack...)
	r.scopeMu.RUnlock()
	for _, scope := range stack {
		scope.appendCollectedRoutes(rules...)
	}
}

func (r *Route) withScope(group *RuleGroup, callback func()) {
	if r == nil || group == nil || group.group == nil || callback == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	r.scopeMu.Lock()
	r.scopeStack = append(r.scopeStack, group)
	r.scopeMu.Unlock()
	defer func() {
		r.scopeMu.Lock()
		for index := len(r.scopeStack) - 1; index >= 0; index-- {
			if r.scopeStack[index] != group {
				continue
			}
			r.scopeStack = append(r.scopeStack[:index], r.scopeStack[index+1:]...)
			break
		}
		r.scopeMu.Unlock()
	}()
	callback()
}

func invokeRouteGroupCallback(callback interface{}, group *RuleGroup) {
	switch typed := callback.(type) {
	case func():
		if typed == nil {
			panic(frameworkRoute.ErrInvalidRoute)
		}
		typed()
	case func(*RuleGroup):
		if typed == nil {
			panic(frameworkRoute.ErrInvalidRoute)
		}
		typed(group)
	default:
		panic(fmt.Errorf("%w: 分组回调类型 %T 非法", frameworkRoute.ErrInvalidRoute, callback))
	}
}

// Routes 返回稳定的路由元数据快照，供 route:list 和诊断使用。
func (r *Route) Routes() ([]frameworkRoute.RouteInfo, error) {
	if r == nil || r.router == nil {
		return nil, frameworkRoute.ErrInvalidRoute
	}
	return r.router.Routes()
}

// Match 执行底层路由匹配，供 HTTP 内核和集成测试使用。
func (r *Route) Match(request *frameworkContext.Request) (*frameworkRoute.Route, map[string]string, error) {
	if r == nil || r.router == nil {
		return nil, nil, frameworkRoute.ErrInvalidRoute
	}
	return r.router.Match(request)
}

// URL 根据命名路由生成 URL。
func (r *Route) URL(name string, params map[string]interface{}) (string, error) {
	if r == nil || r.router == nil {
		return "", frameworkRoute.ErrInvalidRoute
	}
	return r.router.URL(name, params)
}

// URLForRequest 根据命名路由和当前请求生成 URL。
func (r *Route) URLForRequest(request *frameworkContext.Request, name string, params map[string]interface{}) (string, error) {
	if r == nil || r.router == nil {
		return "", frameworkRoute.ErrInvalidRoute
	}
	return r.router.URLForRequest(request, name, params)
}

// Name 设置路由标识并返回当前规则。
func (r *RuleItem) Name(name string) *RuleItem {
	if r == nil || len(r.rules) == 0 {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	// 多方法规则共享一个业务标识，首条规则承担 URL 反解入口。
	mustRouteConfiguration(r.rules[0].WithName(name))
	return r
}

// Pattern 设置路由变量规则并返回当前规则。
func (r *RuleItem) Pattern(patterns map[string]string) *RuleItem {
	if r == nil || len(r.rules) == 0 {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	for _, rule := range r.rules {
		for name, pattern := range patterns {
			mustRouteConfiguration(rule.WithPattern(name, pattern))
		}
	}
	return r
}

// Where 设置路由变量规则并返回当前规则，对应 ThinkPHP Rule::where。
func (r *RuleItem) Where(patterns map[string]string) *RuleItem {
	return r.Pattern(patterns)
}

// Domain 设置精确域名约束并返回当前规则。
func (r *RuleItem) Domain(domain string) *RuleItem {
	if r == nil || len(r.rules) == 0 {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	for _, rule := range r.rules {
		mustRouteConfiguration(rule.WithDomain(domain))
	}
	return r
}

// Ext 设置 URL 后缀约束并返回当前规则。
func (r *RuleItem) Ext(extension string) *RuleItem {
	if r == nil || len(r.rules) == 0 {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	for _, rule := range r.rules {
		mustRouteConfiguration(rule.WithExtension(extension))
	}
	return r
}

// Middleware 追加路由中间件并返回当前规则。
func (r *RuleItem) Middleware(handlers ...middleware.Handler) *RuleItem {
	if r == nil || len(r.rules) == 0 {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	for _, rule := range r.rules {
		mustRouteConfiguration(rule.WithMiddleware(handlers...))
	}
	return r
}

// NamedMiddleware 为当前规则的各方法追加同名中间件。
func (r *RuleItem) NamedMiddleware(name string, handler middleware.Handler) *RuleItem {
	if r == nil || len(r.rules) == 0 {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	for _, rule := range r.rules {
		mustRouteConfiguration(rule.WithNamedMiddleware(name, handler))
	}
	return r
}

// WithoutNamedMiddleware 按注册名称移除当前规则的中间件。
func (r *RuleItem) WithoutNamedMiddleware(names ...string) *RuleItem {
	if r == nil || len(r.rules) == 0 {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	for _, rule := range r.rules {
		mustRouteConfiguration(rule.WithoutNamedMiddleware(names...))
	}
	return r
}

// WithoutMiddleware 保留旧入口，无法确认函数身份时触发注册错误。
// Deprecated: 使用 NamedMiddleware 和 WithoutNamedMiddleware。
func (r *RuleItem) WithoutMiddleware(handlers ...middleware.Handler) *RuleItem {
	if r == nil || len(r.rules) == 0 {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	for _, rule := range r.rules {
		mustRouteConfiguration(rule.WithoutMiddleware(handlers...))
	}
	return r
}

// Path 返回规范化后的路由路径。
func (r *RuleItem) Path() string {
	if r == nil || len(r.rules) == 0 {
		return ""
	}
	return r.rules[0].Path()
}

// Method 返回路由方法；多方法规则返回以竖线连接的方法列表。
func (r *RuleItem) Method() string {
	if r == nil || len(r.rules) == 0 {
		return ""
	}
	methods := make([]string, 0, len(r.rules))
	for _, rule := range r.rules {
		methods = append(methods, rule.Method())
	}
	return strings.Join(methods, "|")
}

// Add 注册分组内指定 HTTP 方法路由。
func (g *RuleGroup) Add(method, rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	registered, err := g.require().Add(method, rule, handler, handlers...)
	item := mustRouteRule(registered, err)
	g.recordRoutes(registered)
	return item
}

// Get 注册分组内 GET 路由。
func (g *RuleGroup) Get(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return g.Add(http.MethodGet, rule, handler, handlers...)
}

// Post 注册分组内 POST 路由。
func (g *RuleGroup) Post(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return g.Add(http.MethodPost, rule, handler, handlers...)
}

// Put 注册分组内 PUT 路由。
func (g *RuleGroup) Put(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return g.Add(http.MethodPut, rule, handler, handlers...)
}

// Delete 注册分组内 DELETE 路由。
func (g *RuleGroup) Delete(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return g.Add(http.MethodDelete, rule, handler, handlers...)
}

// Patch 注册分组内 PATCH 路由。
func (g *RuleGroup) Patch(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return g.Add(http.MethodPatch, rule, handler, handlers...)
}

// Head 注册分组内 HEAD 路由。
func (g *RuleGroup) Head(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return g.Add(http.MethodHead, rule, handler, handlers...)
}

// Options 注册分组内 OPTIONS 路由。
func (g *RuleGroup) Options(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return g.Add(http.MethodOptions, rule, handler, handlers...)
}

// Any 注册分组内任意方法路由。
func (g *RuleGroup) Any(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return g.Add("*", rule, handler, handlers...)
}

// Rule 注册分组内一个或多个 HTTP 方法。
func (g *RuleGroup) Rule(rule string, handler frameworkRoute.HandlerFunc, methods ...string) *RuleItem {
	method := "*"
	if len(methods) > 1 {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	if len(methods) == 1 {
		method = strings.TrimSpace(methods[0])
	}
	registered, err := g.require().Rule(rule, handler, method)
	item := mustRouteRules(registered, err)
	g.recordRoutes(registered...)
	return item
}

// Redirect 注册分组内重定向路由。
func (g *RuleGroup) Redirect(rule, target string, status ...int) *RuleItem {
	if len(status) == 0 {
		status = []int{301}
	}
	registered, err := g.require().Redirect(rule, target, status...)
	item := mustRouteRule(registered, err)
	g.recordRoutes(registered)
	return item
}

// Miss 注册分组内未匹配路由处理器，继承当前分组的前缀、域名、扩展名与中间件。
func (g *RuleGroup) Miss(handler frameworkRoute.HandlerFunc, methods ...string) *RuleItem {
	method := "*"
	if len(methods) > 1 {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	if len(methods) == 1 {
		method = methods[0]
	}
	registered, err := g.require().SetMiss(method, handler)
	item := mustRouteRule(registered, err)
	g.recordRoutes(registered)
	return item
}

// Resource 注册分组内资源路由。
func (g *RuleGroup) Resource(rule, controller string) *Resource {
	resource, err := g.require().Resource(rule, controller)
	mustRouteConfiguration(err)
	g.recordRoutes(resource.Routes()...)
	return &Resource{resource: resource}
}

// Middleware 为当前分组追加匿名中间件；对已完成回调的分组会同步回填全部组内已注册路由。
func (g *RuleGroup) Middleware(handlers ...middleware.Handler) *RuleGroup {
	if g != nil && g.completed {
		group, err := g.require().ApplyMiddlewareToRoutes(g.snapshotRules(), handlers...)
		mustRouteConfiguration(err)
		return g.withUpdatedGroup(group)
	}
	group, err := g.require().WithMiddleware(handlers...)
	mustRouteConfiguration(err)
	return &RuleGroup{route: g.route, group: group}
}

// NamedMiddleware 返回继承命名中间件的注册分组；对已完成回调的分组会同步回填全部组内已注册路由。
func (g *RuleGroup) NamedMiddleware(name string, handler middleware.Handler) *RuleGroup {
	if g != nil && g.completed {
		group, err := g.require().ApplyNamedMiddlewareToRoutes(g.snapshotRules(), name, handler)
		mustRouteConfiguration(err)
		return g.withUpdatedGroup(group)
	}
	group, err := g.require().WithNamedMiddleware(name, handler)
	mustRouteConfiguration(err)
	return &RuleGroup{route: g.route, group: group}
}

// WithoutNamedMiddleware 按注册名称移除当前分组及组内已注册路由的中间件。
func (g *RuleGroup) WithoutNamedMiddleware(names ...string) *RuleGroup {
	if g != nil && g.completed {
		group, err := g.require().ApplyWithoutNamedMiddlewareToRoutes(g.snapshotRules(), names...)
		mustRouteConfiguration(err)
		return g.withUpdatedGroup(group)
	}
	group, err := g.require().WithoutNamedMiddleware(names...)
	mustRouteConfiguration(err)
	return &RuleGroup{route: g.route, group: group}
}

// WithoutMiddleware 保留旧入口，无法确认函数身份时触发注册错误。
// Deprecated: 使用 NamedMiddleware 和 WithoutNamedMiddleware。
func (g *RuleGroup) WithoutMiddleware(handlers ...middleware.Handler) *RuleGroup {
	group, err := g.require().WithoutMiddleware(handlers...)
	mustRouteConfiguration(err)
	if g.completed {
		return g.withUpdatedGroup(group)
	}
	return &RuleGroup{route: g.route, group: group}
}

// Ext 为当前分组设置扩展名约束，并回填已完成回调的组内已注册路由。
func (g *RuleGroup) Ext(extension string) *RuleGroup {
	if g != nil && g.completed {
		group, err := g.require().ApplyExtensionToRoutes(g.snapshotRules(), extension)
		mustRouteConfiguration(err)
		return g.withUpdatedGroup(group)
	}
	group, err := g.require().WithExtension(extension)
	mustRouteConfiguration(err)
	return &RuleGroup{route: g.route, group: group}
}

// Pattern 为当前分组设置变量正则约束，并回填已完成回调的组内已注册路由。
func (g *RuleGroup) Pattern(patterns map[string]string) *RuleGroup {
	if g != nil && g.completed {
		group, err := g.require().ApplyPatternsToRoutes(g.snapshotRules(), patterns)
		mustRouteConfiguration(err)
		return g.withUpdatedGroup(group)
	}
	group, err := g.require().WithPatterns(patterns)
	mustRouteConfiguration(err)
	return &RuleGroup{route: g.route, group: group}
}

// Where 为当前分组设置变量正则约束，对应 ThinkPHP RuleGroup::where。
func (g *RuleGroup) Where(patterns map[string]string) *RuleGroup {
	return g.Pattern(patterns)
}

// Group 创建嵌套路由分组。
func (g *RuleGroup) Group(prefix string, callback interface{}, handlers ...middleware.Handler) *RuleGroup {
	if callback == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	var result *RuleGroup
	err := g.require().Group(prefix, func(group *frameworkRoute.Group) error {
		result = newScopedRuleGroup(g.route, group)
		if g.route != nil {
			g.route.withScope(result, func() { invokeRouteGroupCallback(callback, result) })
		} else {
			invokeRouteGroupCallback(callback, result)
		}
		result.completed = true
		return nil
	}, handlers...)
	mustRouteConfiguration(err)
	if g.completed && result != nil {
		g.appendCollectedRoutes(result.snapshotRules()...)
	}
	return result
}

// Domain 设置当前分组的精确域名约束，或在传入回调时创建嵌套域名分组。
func (g *RuleGroup) Domain(domain string, arguments ...interface{}) *RuleGroup {
	if len(arguments) == 0 {
		if g != nil && g.completed {
			group, err := g.require().ApplyDomainToRoutes(g.snapshotRules(), domain)
			mustRouteConfiguration(err)
			return g.withUpdatedGroup(group)
		}
		group, err := g.require().WithDomain(domain)
		mustRouteConfiguration(err)
		return &RuleGroup{route: g.route, group: group}
	}
	callback, handlers := parseRouteGroupArguments(arguments)
	var result *RuleGroup
	err := g.require().Domain(domain, func(group *frameworkRoute.Group) error {
		result = newScopedRuleGroup(g.route, group)
		if g.route != nil {
			g.route.withScope(result, func() { invokeRouteGroupCallback(callback, result) })
		} else {
			invokeRouteGroupCallback(callback, result)
		}
		result.completed = true
		return nil
	}, handlers...)
	mustRouteConfiguration(err)
	if g.completed && result != nil {
		g.appendCollectedRoutes(result.snapshotRules()...)
	}
	return result
}

// DomainGroup 创建嵌套域名与路径前缀分组。
func (g *RuleGroup) DomainGroup(domain, prefix string, callback interface{}, handlers ...middleware.Handler) *RuleGroup {
	if callback == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	var result *RuleGroup
	err := g.require().DomainGroup(domain, prefix, func(group *frameworkRoute.Group) error {
		result = newScopedRuleGroup(g.route, group)
		if g.route != nil {
			g.route.withScope(result, func() { invokeRouteGroupCallback(callback, result) })
		} else {
			invokeRouteGroupCallback(callback, result)
		}
		result.completed = true
		return nil
	}, handlers...)
	mustRouteConfiguration(err)
	if g.completed && result != nil {
		g.appendCollectedRoutes(result.snapshotRules()...)
	}
	return result
}

func (g *RuleGroup) require() *frameworkRoute.Group {
	if g == nil || g.group == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	return g.group
}

// Only 仅保留指定资源动作并返回当前资源路由。
func (r *Resource) Only(actions ...string) *Resource {
	if r == nil || r.resource == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	mustRouteConfiguration(r.resource.Only(actions...))
	return r
}

// Except 排除指定资源动作并返回当前资源路由。
func (r *Resource) Except(actions ...string) *Resource {
	if r == nil || r.resource == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	mustRouteConfiguration(r.resource.Except(actions...))
	return r
}
