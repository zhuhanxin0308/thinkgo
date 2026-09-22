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
	scopeStack []*frameworkRoute.Group
}

// RuleItem 对应 ThinkPHP 的 think\route\RuleItem，并提供链式规则配置。
type RuleItem struct {
	rules []*frameworkRoute.Route
}

// RuleGroup 对应 ThinkPHP 的 think\route\RuleGroup。
type RuleGroup struct {
	route *Route
	group *frameworkRoute.Group
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

// Add 注册指定 HTTP 方法的路由。
func (r *Route) Add(method, rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	if r == nil || r.router == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	if group := r.currentScope(); group != nil {
		return mustRouteRule(group.Add(method, rule, handler, handlers...))
	}
	return mustRouteRule(r.router.Add(method, rule, handler, handlers...))
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
	if group := r.currentScope(); group != nil {
		return mustRouteRules(group.Rule(rule, handler, method))
	}
	return mustRouteRules(r.router.Rule(rule, handler, method))
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
		return frameworkContext.NewResponse().Content(content)
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
	if group := r.currentScope(); group != nil {
		return mustRouteRule(group.Redirect(rule, target, status...))
	}
	return mustRouteRule(r.router.Redirect(rule, target, status...))
}

// Miss 注册未匹配路由处理器，并支持 ThinkPHP 的 method 可选参数。
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
	return mustRouteRule(r.router.SetMiss(method, handler))
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
	return &Resource{resource: resource}
}

// Group 创建路由前缀分组。回调既支持 ThinkPHP 原生的 func() 写法，也保留
// 显式接收 RuleGroup 的 Go 强类型写法。
func (r *Route) Group(prefix string, callback interface{}, handlers ...middleware.Handler) *RuleGroup {
	if r == nil || r.router == nil || callback == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	var result *RuleGroup
	register := func(group *frameworkRoute.Group) error {
		result = &RuleGroup{route: r, group: group}
		r.withScope(group, func() { invokeRouteGroupCallback(callback, result) })
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

// Domain 创建域名路由分组。
func (r *Route) Domain(domain string, callback interface{}, handlers ...middleware.Handler) *RuleGroup {
	if r == nil || r.router == nil || callback == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	var result *RuleGroup
	register := func(group *frameworkRoute.Group) error {
		result = &RuleGroup{route: r, group: group}
		r.withScope(group, func() { invokeRouteGroupCallback(callback, result) })
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
		result = &RuleGroup{route: r, group: group}
		r.withScope(group, func() { invokeRouteGroupCallback(callback, result) })
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
	return r.scopeStack[len(r.scopeStack)-1]
}

func (r *Route) withScope(group *frameworkRoute.Group, callback func()) {
	if r == nil || group == nil || callback == nil {
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

// WithoutMiddleware 移除路由中间件并返回当前规则。
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

// Get 注册分组内 GET 路由。
func (g *RuleGroup) Get(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return mustRouteRule(g.require().Get(rule, handler, handlers...))
}

// Post 注册分组内 POST 路由。
func (g *RuleGroup) Post(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return mustRouteRule(g.require().Post(rule, handler, handlers...))
}

// Put 注册分组内 PUT 路由。
func (g *RuleGroup) Put(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return mustRouteRule(g.require().Put(rule, handler, handlers...))
}

// Delete 注册分组内 DELETE 路由。
func (g *RuleGroup) Delete(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return mustRouteRule(g.require().Delete(rule, handler, handlers...))
}

// Patch 注册分组内 PATCH 路由。
func (g *RuleGroup) Patch(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return mustRouteRule(g.require().Patch(rule, handler, handlers...))
}

// Head 注册分组内 HEAD 路由。
func (g *RuleGroup) Head(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return mustRouteRule(g.require().Head(rule, handler, handlers...))
}

// Options 注册分组内 OPTIONS 路由。
func (g *RuleGroup) Options(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return mustRouteRule(g.require().Options(rule, handler, handlers...))
}

// Any 注册分组内任意方法路由。
func (g *RuleGroup) Any(rule string, handler frameworkRoute.HandlerFunc, handlers ...middleware.Handler) *RuleItem {
	return mustRouteRule(g.require().Any(rule, handler, handlers...))
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
	return mustRouteRules(g.require().Rule(rule, handler, method))
}

// Redirect 注册分组内重定向路由。
func (g *RuleGroup) Redirect(rule, target string, status ...int) *RuleItem {
	if len(status) == 0 {
		status = []int{301}
	}
	return mustRouteRule(g.require().Redirect(rule, target, status...))
}

// Resource 注册分组内资源路由。
func (g *RuleGroup) Resource(rule, controller string) *Resource {
	resource, err := g.require().Resource(rule, controller)
	mustRouteConfiguration(err)
	return &Resource{resource: resource}
}

// Group 创建嵌套路由分组。
func (g *RuleGroup) Group(prefix string, callback interface{}, handlers ...middleware.Handler) *RuleGroup {
	if callback == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	var result *RuleGroup
	err := g.require().Group(prefix, func(group *frameworkRoute.Group) error {
		result = &RuleGroup{route: g.route, group: group}
		if g.route != nil {
			g.route.withScope(group, func() { invokeRouteGroupCallback(callback, result) })
		} else {
			invokeRouteGroupCallback(callback, result)
		}
		return nil
	}, handlers...)
	mustRouteConfiguration(err)
	return result
}

// Domain 创建嵌套域名分组。
func (g *RuleGroup) Domain(domain string, callback interface{}, handlers ...middleware.Handler) *RuleGroup {
	if callback == nil {
		panic(frameworkRoute.ErrInvalidRoute)
	}
	var result *RuleGroup
	err := g.require().Domain(domain, func(group *frameworkRoute.Group) error {
		result = &RuleGroup{route: g.route, group: group}
		if g.route != nil {
			g.route.withScope(group, func() { invokeRouteGroupCallback(callback, result) })
		} else {
			invokeRouteGroupCallback(callback, result)
		}
		return nil
	}, handlers...)
	mustRouteConfiguration(err)
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
