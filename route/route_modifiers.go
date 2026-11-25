package route

import (
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	"github.com/zhuhanxin0308/thinkgo/v3/context"
)

// WithDomain 派生精确域名约束的分组作用域。
func (g *Group) WithDomain(domain string) (*Group, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	normalized, err := normalizeAndValidateRouteDomain(domain)
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return nil, fmt.Errorf("%w: 单路由域名不能为空", ErrInvalidRouteDomain)
	}
	g.router.mu.RLock()
	defer g.router.mu.RUnlock()
	if g.router.frozen.Load() {
		return nil, ErrRouterFrozen
	}
	next := g.clone()
	next.scopeDepth = g.effectiveScopeDepth() + 1
	next.domain = normalized
	next.domainDepth = next.scopeDepth
	return next, nil
}

// ApplyDomainToRoutes 将分组后置域名约束原子回填到未显式覆盖域名的组内路由。
func (g *Group) ApplyDomainToRoutes(routes []*Route, domain string) (*Group, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	normalized, err := normalizeAndValidateRouteDomain(domain)
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return nil, fmt.Errorf("%w: 单路由域名不能为空", ErrInvalidRouteDomain)
	}
	g.router.mu.Lock()
	defer g.router.mu.Unlock()
	if g.router.frozen.Load() {
		return nil, ErrRouterFrozen
	}
	level := g.effectiveScopeDepth()
	active := uniqueActiveRoutes(routes)
	mutating := make(map[*Route]struct{}, len(active))
	candidates := make([]*Route, 0, len(active))
	for _, registered := range active {
		if registered.explicitDomain || registered.domainDepth > level {
			continue
		}
		if registered.jsonContract {
			return nil, fmt.Errorf("%w: JSON 契约路由不支持分组域名约束", ErrInvalidRouteDomain)
		}
		if registered.isMiss {
			continue
		}
		mutating[registered] = struct{}{}
		copied := *registered
		copied.domain = normalized
		candidates = append(candidates, &copied)
	}
	for _, candidate := range candidates {
		if g.router.findBatchMutationDuplicateLocked(candidate, mutating, candidates) != nil {
			return nil, fmt.Errorf("%w: %s %s domain=%q", ErrDuplicateRoute, candidate.method, candidate.displayPath(), normalized)
		}
	}
	for _, registered := range active {
		if !registered.explicitDomain && registered.domainDepth <= level {
			registered.domain = normalized
			registered.domainDepth = level
			if registered.isMiss {
				if g.router.missRoute == registered {
					g.router.missRoute = nil
				}
				if g.router.missRoutes[registered.method] == registered {
					delete(g.router.missRoutes, registered.method)
				}
			}
		}
	}
	next := g.clone()
	next.scopeDepth = level
	next.domain = normalized
	next.domainDepth = level
	return next, nil
}

// WithExtension 派生扩展名约束的分组作用域。
func (g *Group) WithExtension(extension string) (*Group, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	normalized, err := normalizeRouteExtension(extension)
	if err != nil {
		return nil, err
	}
	g.router.mu.RLock()
	defer g.router.mu.RUnlock()
	if g.router.frozen.Load() {
		return nil, ErrRouterFrozen
	}
	next := g.clone()
	next.scopeDepth = g.effectiveScopeDepth() + 1
	next.ext = normalized
	next.extensionRequired = true
	next.extDepth = next.scopeDepth
	return next, nil
}

// ApplyExtensionToRoutes 将分组后置扩展名约束原子回填到未显式覆盖扩展名的组内路由。
func (g *Group) ApplyExtensionToRoutes(routes []*Route, extension string) (*Group, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	normalized, err := normalizeRouteExtension(extension)
	if err != nil {
		return nil, err
	}
	g.router.mu.Lock()
	defer g.router.mu.Unlock()
	if g.router.frozen.Load() {
		return nil, ErrRouterFrozen
	}
	level := g.effectiveScopeDepth()
	active := uniqueActiveRoutes(routes)
	mutating := make(map[*Route]struct{}, len(active))
	candidates := make([]*Route, 0, len(active))
	for _, registered := range active {
		if registered.explicitExt || registered.extDepth > level {
			continue
		}
		if registered.jsonContract {
			return nil, fmt.Errorf("%w: JSON 契约路由不支持分组强制后缀约束", ErrInvalidRoute)
		}
		if registered.isMiss {
			continue
		}
		mutating[registered] = struct{}{}
		copied := *registered
		copied.ext = normalized
		copied.extensionRequired = true
		candidates = append(candidates, &copied)
	}
	for _, candidate := range candidates {
		if g.router.findBatchMutationDuplicateLocked(candidate, mutating, candidates) != nil {
			return nil, fmt.Errorf("%w: %s %s extension=%q", ErrDuplicateRoute, candidate.method, candidate.displayPath(), normalized)
		}
	}
	for _, registered := range active {
		if !registered.explicitExt && registered.extDepth <= level {
			registered.ext = normalized
			registered.extensionRequired = true
			registered.extDepth = level
			if registered.isMiss {
				if g.router.missRoute == registered {
					g.router.missRoute = nil
				}
				if g.router.missRoutes[registered.method] == registered {
					delete(g.router.missRoutes, registered.method)
				}
			}
		}
	}
	next := g.clone()
	next.scopeDepth = level
	next.ext = normalized
	next.extensionRequired = true
	next.extDepth = level
	return next, nil
}

// WithPatterns 派生携带变量正则约束的分组作用域。
func (g *Group) WithPatterns(patterns map[string]string) (*Group, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	compiledMap, normalizedMap, err := compileGroupPatterns(patterns)
	if err != nil {
		return nil, err
	}
	_ = compiledMap
	g.router.mu.RLock()
	defer g.router.mu.RUnlock()
	if g.router.frozen.Load() {
		return nil, ErrRouterFrozen
	}
	next := g.clone()
	next.scopeDepth = g.effectiveScopeDepth() + 1
	if len(normalizedMap) > 0 {
		if next.patterns == nil {
			next.patterns = make(map[string]string, len(normalizedMap))
		}
		if next.patternDepth == nil {
			next.patternDepth = make(map[string]int, len(normalizedMap))
		}
		for param, pattern := range normalizedMap {
			next.patterns[param] = pattern
			next.patternDepth[param] = next.scopeDepth
		}
	}
	return next, nil
}

// ApplyPatternsToRoutes 将分组后置变量正则约束原子回填到包含对应变量且未显式覆盖约束的组内路由。
func (g *Group) ApplyPatternsToRoutes(routes []*Route, patterns map[string]string) (*Group, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	compiledMap, normalizedMap, err := compileGroupPatterns(patterns)
	if err != nil {
		return nil, err
	}
	g.router.mu.Lock()
	defer g.router.mu.Unlock()
	if g.router.frozen.Load() {
		return nil, ErrRouterFrozen
	}
	level := g.effectiveScopeDepth()
	keys := make([]string, 0, len(normalizedMap))
	for param := range normalizedMap {
		keys = append(keys, param)
	}
	sort.Strings(keys)
	for _, registered := range uniqueActiveRoutes(routes) {
		targetPath := registered.path
		if registered.isMiss {
			targetPath = registered.missPrefix
		}
		for _, param := range keys {
			if registered.explicitPatterns[param] || registered.patternDepth[param] > level || !routeHasParam(targetPath, param) {
				continue
			}
			if registered.patternDepth == nil {
				registered.patternDepth = make(map[string]int)
			}
			registered.patterns[param] = normalizedMap[param]
			registered.compiled[param] = compiledMap[param]
			registered.patternDepth[param] = level
		}
	}
	next := g.clone()
	next.scopeDepth = level
	if len(normalizedMap) > 0 {
		if next.patterns == nil {
			next.patterns = make(map[string]string, len(normalizedMap))
		}
		if next.patternDepth == nil {
			next.patternDepth = make(map[string]int, len(normalizedMap))
		}
		for param, pattern := range normalizedMap {
			next.patterns[param] = pattern
			next.patternDepth[param] = level
		}
	}
	return next, nil
}

func compileGroupPatterns(patterns map[string]string) (map[string]*regexp.Regexp, map[string]string, error) {
	if len(patterns) == 0 {
		return nil, nil, nil
	}
	keys := make([]string, 0, len(patterns))
	for rawParam := range patterns {
		keys = append(keys, rawParam)
	}
	sort.Strings(keys)
	compiledMap := make(map[string]*regexp.Regexp, len(patterns))
	normalizedMap := make(map[string]string, len(patterns))
	for _, rawParam := range keys {
		param := strings.TrimSpace(rawParam)
		if !isValidAutoRouteSegment(param) || strings.Contains(param, ":") {
			return nil, nil, fmt.Errorf("%w: 参数名 %q 非法", ErrInvalidRoutePattern, rawParam)
		}
		regex, err := compileRoutePattern(param, patterns[rawParam])
		if err != nil {
			return nil, nil, err
		}
		compiledMap[param] = regex
		normalizedMap[param] = patterns[rawParam]
	}
	return compiledMap, normalizedMap, nil
}

func compileRoutePattern(param, pattern string) (*regexp.Regexp, error) {
	if pattern == "" || len(pattern) > maxRoutePatternLength || strings.Contains(pattern, "(?P<") || strings.Contains(pattern, "(?<") {
		return nil, fmt.Errorf("%w: 参数 %q 的约束非法", ErrInvalidRoutePattern, param)
	}
	compiled, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return nil, fmt.Errorf("%w: 参数 %q: %v", ErrInvalidRoutePattern, param, err)
	}
	return compiled, nil
}

// mutableRouter 返回路由所属的可配置路由器，零值路由统一以错误方式安全失败。
func (r *Route) mutableRouter() (*Router, error) {
	if r == nil || r.router == nil {
		return nil, ErrInvalidRoute
	}
	return r.router, nil
}

// displayPath 返回包含显式末尾斜杠标记的路由路径，供快照、错误提示和命名路由缓存统一使用。
func (r *Route) displayPath() string {
	if r == nil {
		return ""
	}
	if r.trailingSlash && r.path != "" && r.path != "/" && !strings.HasSuffix(r.path, "/") {
		return r.path + "/"
	}
	return r.path
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
	if router.frozen.Load() {
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
	targetPath := r.path
	if r.isMiss {
		targetPath = r.missPrefix
	}
	if !routeHasParam(targetPath, param) {
		return fmt.Errorf("%w: 路径 %s 不包含参数 %q", ErrInvalidRoutePattern, targetPath, param)
	}
	compiled, err := compileRoutePattern(param, pattern)
	if err != nil {
		return err
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.frozen.Load() {
		return ErrRouterFrozen
	}
	if r.explicitPatterns == nil {
		r.explicitPatterns = make(map[string]bool)
	}
	r.patterns[param] = pattern
	r.compiled[param] = compiled
	r.explicitPatterns[param] = true
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
	if router.frozen.Load() {
		return ErrRouterFrozen
	}
	if !r.isMiss {
		candidate := *r
		candidate.domain = normalized
		if router.findDuplicateLocked(&candidate, r, nil) != nil {
			return fmt.Errorf("%w: %s %s domain=%q", ErrDuplicateRoute, r.method, r.displayPath(), normalized)
		}
	}
	r.domain = normalized
	r.explicitDomain = true
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
	if router.frozen.Load() {
		return ErrRouterFrozen
	}
	if !r.isMiss {
		candidate := *r
		candidate.ext = normalized
		if router.findDuplicateLocked(&candidate, r, nil) != nil {
			return fmt.Errorf("%w: %s %s extension=%q", ErrDuplicateRoute, r.method, r.displayPath(), normalized)
		}
	}
	r.ext = normalized
	r.extensionRequired = true
	r.explicitExt = true
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
	return r.displayPath()
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
	target.removed = true
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
