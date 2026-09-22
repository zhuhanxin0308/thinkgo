package http

import (
	stdcontext "context"
	"errors"
	"fmt"
	"net"
	stdhttp "net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

var (
	errApplicationNotFound = errors.New("应用不存在")
	errApplicationDenied   = errors.New("应用禁止访问")
	errInvalidApplication  = errors.New("应用解析配置非法")
	errInvalidAppRequest   = errors.New("应用请求非法")
)

type applicationResolution struct {
	name          string
	originalPath  string
	rewrittenPath string
	pathPrefix    string
	urlPrefix     string
	canonicalHost string
	domainBound   bool
}

func (resolution applicationResolution) requestContext(host string) fwcontext.ApplicationContext {
	return fwcontext.NewApplicationContextWithURLPrefix(
		resolution.name,
		resolution.originalPath,
		resolution.rewrittenPath,
		resolution.pathPrefix,
		resolution.urlPrefix,
		host,
		resolution.canonicalHost,
		resolution.domainBound,
	)
}

type applicationDomainBinding struct {
	pattern string
	target  string
}

// applicationResolver 是启动期构建的只读应用选择规则。
type applicationResolver struct {
	applications         map[string]struct{}
	defaultApp           string
	explicitApp          string
	appMap               map[string]string
	domainBindings       map[string]string
	subdomainBindings    map[string]string
	wildcardBindings     []applicationDomainBinding
	wildcardDomainTarget string
	denied               map[string]struct{}
	appExpress           bool
	singleApplication    bool
}

func newApplicationResolver(applications map[string]*Http, configuration map[string]interface{}, explicitApp string) (*applicationResolver, error) {
	if len(applications) == 0 {
		return nil, framework.ErrNoApplications
	}
	resolver := &applicationResolver{
		applications:      make(map[string]struct{}, len(applications)),
		appMap:            make(map[string]string),
		domainBindings:    make(map[string]string),
		subdomainBindings: make(map[string]string),
		denied:            make(map[string]struct{}),
		singleApplication: len(applications) == 1,
	}
	names := make([]string, 0, len(applications))
	for name := range applications {
		resolver.applications[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	resolver.defaultApp = names[0]
	if _, exists := resolver.applications["index"]; exists {
		resolver.defaultApp = "index"
	}
	if configuration != nil {
		if rawDefault, exists := configuration["default_app"]; exists {
			configured, ok := rawDefault.(string)
			if !ok {
				return nil, fmt.Errorf("%w: default_app 必须是字符串", errInvalidApplication)
			}
			if configured = strings.TrimSpace(configured); configured != "" {
				resolver.defaultApp = configured
			}
		}
	}
	if _, exists := resolver.applications[resolver.defaultApp]; !exists {
		return nil, fmt.Errorf("%w: 默认应用 %q 未发现", errInvalidApplication, resolver.defaultApp)
	}

	resolver.explicitApp = strings.TrimSpace(explicitApp)
	if resolver.explicitApp != "" {
		if _, exists := resolver.applications[resolver.explicitApp]; !exists {
			return nil, fmt.Errorf("%w: 绑定应用 %q 未发现", errInvalidApplication, resolver.explicitApp)
		}
	}
	var err error
	if configuration != nil {
		resolver.denied, err = parseDeniedApplications(configuration["deny_app_list"], resolver.applications)
		if err != nil {
			return nil, err
		}
		resolver.appMap, err = parseApplicationMap(configuration["app_map"], resolver.applications)
		if err != nil {
			return nil, err
		}
		if err := resolver.parseDomainBindings(configuration["domain_bind"]); err != nil {
			return nil, err
		}
		rawExpress := configuration["app_express"]
		resolver.appExpress, err = parseApplicationExpress(rawExpress)
		if err != nil {
			return nil, err
		}
		if rawExpress == nil && resolver.singleApplication {
			resolver.appExpress = true
		}
	} else if resolver.singleApplication {
		resolver.appExpress = true
	}
	if _, denied := resolver.denied[resolver.defaultApp]; denied {
		return nil, fmt.Errorf("%w: 默认应用 %q 不能禁止访问", errInvalidApplication, resolver.defaultApp)
	}
	return resolver, nil
}

func (resolver *applicationResolver) resolve(request *stdhttp.Request) (applicationResolution, error) {
	if resolver == nil || request == nil || request.URL == nil {
		return applicationResolution{}, fmt.Errorf("%w: 请求不能为空", errInvalidAppRequest)
	}
	originalPath, err := validateApplicationRequestPath(request.URL)
	if err != nil {
		return applicationResolution{}, err
	}
	host, err := normalizeApplicationHost(request.Host)
	if err != nil {
		return applicationResolution{}, err
	}
	if resolver.explicitApp != "" {
		return resolver.boundResolution(resolver.explicitApp, originalPath, host), nil
	}
	if target, canonicalHost, matched := resolver.resolveDomain(host); matched {
		if err := resolver.ensureAccessible(target); err != nil {
			return applicationResolution{}, err
		}
		return resolver.boundResolution(target, originalPath, canonicalHost), nil
	}

	segment, hasSegment := firstApplicationPathSegment(originalPath)
	if !hasSegment {
		return resolver.defaultResolution(originalPath), nil
	}
	if _, denied := resolver.denied[segment]; denied {
		return applicationResolution{}, fmt.Errorf("%w: %q", errApplicationDenied, segment)
	}

	target, known := resolver.appMap[segment]
	if !known {
		if resolver.applicationMapContainsTarget(segment) {
			return applicationResolution{}, fmt.Errorf("%w: %q", errApplicationNotFound, segment)
		}
		if _, exists := resolver.applications[segment]; exists {
			target = segment
			known = true
		} else if wildcard := resolver.appMap["*"]; wildcard != "" {
			target = wildcard
			known = true
		}
	}
	if !known {
		if resolver.appExpress {
			return resolver.directDefaultResolution(originalPath), nil
		}
		return applicationResolution{}, fmt.Errorf("%w: %q", errApplicationNotFound, segment)
	}
	if err := resolver.ensureAccessible(target); err != nil {
		return applicationResolution{}, err
	}
	return applicationResolution{
		name:          target,
		originalPath:  originalPath,
		rewrittenPath: rewriteApplicationPath(originalPath, segment),
		pathPrefix:    "/" + segment,
		urlPrefix:     "/" + segment,
	}, nil
}

func (resolver *applicationResolver) defaultResolution(path string) applicationResolution {
	urlPrefix := ""
	if !resolver.singleApplication {
		urlPrefix = "/" + resolver.visibleApplicationName(resolver.defaultApp)
	}
	return applicationResolution{name: resolver.defaultApp, originalPath: path, rewrittenPath: path, urlPrefix: urlPrefix}
}

func (resolver *applicationResolver) directDefaultResolution(path string) applicationResolution {
	return applicationResolution{name: resolver.defaultApp, originalPath: path, rewrittenPath: path}
}

func (resolver *applicationResolver) boundResolution(name, path, host string) applicationResolution {
	return applicationResolution{
		name: name, originalPath: path, rewrittenPath: path,
		canonicalHost: host, domainBound: true,
	}
}

func (resolver *applicationResolver) ensureAccessible(name string) error {
	if _, denied := resolver.denied[name]; denied {
		return fmt.Errorf("%w: %q", errApplicationDenied, name)
	}
	if _, exists := resolver.applications[name]; !exists {
		return fmt.Errorf("%w: %q", errApplicationNotFound, name)
	}
	return nil
}

func (resolver *applicationResolver) visibleApplicationName(name string) string {
	aliases := make([]string, 0)
	for alias, target := range resolver.appMap {
		if alias != "*" && target == name {
			aliases = append(aliases, alias)
		}
	}
	if len(aliases) == 0 {
		return name
	}
	sort.Strings(aliases)
	return aliases[0]
}

func (resolver *applicationResolver) applicationMapContainsTarget(name string) bool {
	for _, target := range resolver.appMap {
		if target == name {
			return true
		}
	}
	return false
}

func (resolver *applicationResolver) resolveDomain(host string) (string, string, bool) {
	if host == "" {
		return "", "", false
	}
	if target, exists := resolver.domainBindings[host]; exists {
		return target, host, true
	}
	if dot := strings.IndexByte(host, '.'); dot > 0 {
		if target, exists := resolver.subdomainBindings[host[:dot]]; exists {
			return target, host, true
		}
	}
	for _, binding := range resolver.wildcardBindings {
		if strings.HasSuffix(host, binding.pattern) && host != strings.TrimPrefix(binding.pattern, ".") {
			return binding.target, host, true
		}
	}
	if resolver.wildcardDomainTarget != "" {
		return resolver.wildcardDomainTarget, host, true
	}
	return "", "", false
}

func (resolver *applicationResolver) parseDomainBindings(raw interface{}) error {
	values, err := applicationStringMap(raw, "domain_bind")
	if err != nil {
		return err
	}
	for pattern, target := range values {
		if err := resolver.ensureAccessible(target); err != nil {
			return fmt.Errorf("%w: domain_bind %q: %v", errInvalidApplication, pattern, err)
		}
		normalized, kind, err := normalizeApplicationDomainPattern(pattern)
		if err != nil {
			return fmt.Errorf("%w: %v", errInvalidApplication, err)
		}
		switch kind {
		case "all":
			if resolver.wildcardDomainTarget != "" {
				return fmt.Errorf("%w: domain_bind 通配规则重复", errInvalidApplication)
			}
			resolver.wildcardDomainTarget = target
		case "subdomain":
			if _, exists := resolver.subdomainBindings[normalized]; exists {
				return fmt.Errorf("%w: 子域名 %q 重复", errInvalidApplication, pattern)
			}
			resolver.subdomainBindings[normalized] = target
		case "wildcard":
			for _, existing := range resolver.wildcardBindings {
				if existing.pattern == normalized {
					return fmt.Errorf("%w: 通配域名 %q 标准化后重复", errInvalidApplication, pattern)
				}
			}
			resolver.wildcardBindings = append(resolver.wildcardBindings, applicationDomainBinding{pattern: normalized, target: target})
		default:
			if _, exists := resolver.domainBindings[normalized]; exists {
				return fmt.Errorf("%w: 域名 %q 重复", errInvalidApplication, pattern)
			}
			resolver.domainBindings[normalized] = target
		}
	}
	sort.SliceStable(resolver.wildcardBindings, func(left, right int) bool {
		return len(resolver.wildcardBindings[left].pattern) > len(resolver.wildcardBindings[right].pattern)
	})
	return nil
}

func parseApplicationMap(raw interface{}, applications map[string]struct{}) (map[string]string, error) {
	values, err := applicationStringMap(raw, "app_map")
	if err != nil {
		return nil, err
	}
	for alias, target := range values {
		if alias != "*" {
			if err := validateApplicationAlias(alias); err != nil {
				return nil, fmt.Errorf("%w: %v", errInvalidApplication, err)
			}
		}
		if _, exists := applications[target]; !exists {
			return nil, fmt.Errorf("%w: app_map %q 指向未发现应用 %q", errInvalidApplication, alias, target)
		}
	}
	return values, nil
}

func parseDeniedApplications(raw interface{}, applications map[string]struct{}) (map[string]struct{}, error) {
	values, err := applicationStringList(raw, "deny_app_list")
	if err != nil {
		return nil, err
	}
	denied := make(map[string]struct{}, len(values))
	for _, name := range values {
		if _, exists := applications[name]; !exists {
			return nil, fmt.Errorf("%w: deny_app_list 包含未发现应用 %q", errInvalidApplication, name)
		}
		if _, exists := denied[name]; exists {
			return nil, fmt.Errorf("%w: deny_app_list 应用 %q 重复", errInvalidApplication, name)
		}
		denied[name] = struct{}{}
	}
	return denied, nil
}

func parseApplicationExpress(raw interface{}) (bool, error) {
	if raw == nil {
		return false, nil
	}
	switch value := raw.(type) {
	case bool:
		return value, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1":
			return true, nil
		case "false", "0":
			return false, nil
		}
	}
	return false, fmt.Errorf("%w: app_express 必须是布尔值", errInvalidApplication)
}

func applicationStringMap(raw interface{}, field string) (map[string]string, error) {
	if raw == nil {
		return map[string]string{}, nil
	}
	values, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: %s 必须是对象", errInvalidApplication, field)
	}
	result := make(map[string]string, len(values))
	for key, rawValue := range values {
		value, ok := rawValue.(string)
		if !ok || strings.TrimSpace(value) != value || strings.TrimSpace(key) != key {
			return nil, fmt.Errorf("%w: %s 的键和值必须是不含首尾空白的字符串", errInvalidApplication, field)
		}
		result[key] = value
	}
	return result, nil
}

func applicationStringList(raw interface{}, field string) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: %s 必须是数组", errInvalidApplication, field)
	}
	result := make([]string, 0, len(list))
	for _, rawValue := range list {
		value, ok := rawValue.(string)
		if !ok || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("%w: %s 必须是字符串数组", errInvalidApplication, field)
		}
		result = append(result, value)
	}
	return result, nil
}

func normalizeApplicationDomainPattern(pattern string) (string, string, error) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "*" {
		return pattern, "all", nil
	}
	if pattern == "" || strings.ContainsAny(pattern, "/\\\r\n\t") || strings.Contains(pattern, "..") || strings.Count(pattern, "*") > 1 {
		return "", "", fmt.Errorf("域名绑定 %q 包含非法字符", pattern)
	}
	if strings.HasPrefix(pattern, "*.") {
		return strings.TrimPrefix(pattern, "*"), "wildcard", nil
	}
	if strings.Contains(pattern, "*") {
		return "", "", fmt.Errorf("域名绑定 %q 的通配符位置非法", pattern)
	}
	if strings.Contains(pattern, ".") {
		host, err := normalizeApplicationHost(pattern)
		return host, "exact", err
	}
	if err := validateApplicationAlias(pattern); err != nil {
		return "", "", err
	}
	return pattern, "subdomain", nil
}

func validateApplicationAlias(value string) error {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\%\r\n\t") {
		return fmt.Errorf("应用标识 %q 非法", value)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("应用标识 %q 包含控制字符", value)
		}
	}
	return nil
}

func normalizeApplicationHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", nil
	}
	if strings.ContainsAny(host, "/\\\r\n\t") {
		return "", fmt.Errorf("%w: Host 包含非法字符", errInvalidAppRequest)
	}
	if strings.HasPrefix(host, "[") {
		parsedHost, _, err := net.SplitHostPort(host)
		if err == nil {
			host = parsedHost
		} else if strings.HasSuffix(host, "]") {
			host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		} else {
			return "", fmt.Errorf("%w: Host 格式非法", errInvalidAppRequest)
		}
	} else if strings.Count(host, ":") == 1 {
		if parsedHost, _, err := net.SplitHostPort(host); err == nil {
			host = parsedHost
		}
	} else if strings.Count(host, ":") > 1 {
		return "", fmt.Errorf("%w: Host 格式非法", errInvalidAppRequest)
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return "", fmt.Errorf("%w: Host 不能为空", errInvalidAppRequest)
	}
	return host, nil
}

func validateApplicationRequestPath(requestURL *url.URL) (string, error) {
	escaped := strings.ToLower(requestURL.EscapedPath() + " " + requestURL.RawPath)
	for _, marker := range []string{"%2f", "%5c", "%00", "%2e%2e"} {
		if strings.Contains(escaped, marker) {
			return "", fmt.Errorf("%w: URL 路径包含禁止编码", errInvalidAppRequest)
		}
	}
	path := requestURL.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00\r\n") {
		return "", fmt.Errorf("%w: URL 路径格式非法", errInvalidAppRequest)
	}
	return path, nil
}

func firstApplicationPathSegment(path string) (string, bool) {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return "", false
	}
	if index := strings.IndexByte(trimmed, '/'); index >= 0 {
		trimmed = trimmed[:index]
	}
	if dot := strings.IndexByte(trimmed, '.'); dot >= 0 {
		trimmed = trimmed[:dot]
	}
	if trimmed == "" || trimmed == "." || trimmed == ".." {
		return "", false
	}
	return trimmed, true
}

func rewriteApplicationPath(path, _ string) string {
	trimmed := strings.TrimPrefix(path, "/")
	separator := strings.IndexByte(trimmed, '/')
	if separator < 0 {
		return "/"
	}
	remainder := strings.TrimLeft(trimmed[separator+1:], "/")
	if remainder == "" {
		return "/"
	}
	return "/" + remainder
}

type resolvedApplicationContextKey struct{}

func withResolvedApplication(request *stdhttp.Request, application fwcontext.ApplicationContext) *stdhttp.Request {
	if request == nil {
		return nil
	}
	ctx := stdcontext.WithValue(request.Context(), resolvedApplicationContextKey{}, application)
	return request.WithContext(ctx)
}

func resolvedApplication(request *stdhttp.Request) (fwcontext.ApplicationContext, bool) {
	if request == nil {
		return fwcontext.ApplicationContext{}, false
	}
	application, ok := request.Context().Value(resolvedApplicationContextKey{}).(fwcontext.ApplicationContext)
	return application, ok
}

// nativeApplicationHost 是 Http 内部的原生多应用分发器，不暴露第二套运行入口。
type nativeApplicationHost struct {
	resolver     *applicationResolver
	applications map[string]*Http
	endMu        sync.Mutex
	endHandlers  map[*fwcontext.ResponseIdentity]*Http
}

func newNativeApplicationHost(root *Http) (*nativeApplicationHost, error) {
	if root == nil || root.app == nil {
		return nil, framework.ErrNilApplication
	}
	applicationInstances, err := root.app.BuildApplications()
	if err != nil {
		return nil, err
	}
	handlers := make(map[string]*Http, len(applicationInstances))
	for _, name := range root.app.ApplicationNames() {
		application := applicationInstances[name]
		if application == nil {
			return nil, fmt.Errorf("应用 %q 实例不存在", name)
		}
		handler, err := newHttp(application, false)
		if err != nil {
			return nil, fmt.Errorf("创建应用 %q HTTP 内核失败: %w", name, err)
		}
		handler.Name(name)
		handler.SetRoutePath(application.GetRoutePath())
		if err := handler.ensureInitialized(); err != nil {
			return nil, fmt.Errorf("初始化应用 %q HTTP 内核失败: %w", name, err)
		}
		if err := handler.ensureRouteFrozen(); err != nil {
			return nil, fmt.Errorf("加载应用 %q 路由失败: %w", name, err)
		}
		handlers[name] = handler
	}
	resolver, err := newApplicationResolver(handlers, root.app.ProjectApplicationConfig(), root.GetName())
	if err != nil {
		return nil, err
	}
	return &nativeApplicationHost{
		resolver: resolver, applications: handlers,
		endHandlers: make(map[*fwcontext.ResponseIdentity]*Http),
	}, nil
}

// lifecycleHandlers 按应用名返回稳定的子应用处理器顺序，
// 让监听宿主可以确定性地获取和逆序释放运行租约。
func (host *nativeApplicationHost) lifecycleHandlers() []*Http {
	if host == nil {
		return nil
	}
	names := make([]string, 0, len(host.applications))
	for name := range host.applications {
		names = append(names, name)
	}
	sort.Strings(names)
	handlers := make([]*Http, 0, len(names))
	for _, name := range names {
		if handler := host.applications[name]; handler != nil {
			handlers = append(handlers, handler)
		}
	}
	return handlers
}

// resolveRequest 允许无应用前缀的公共文件进入全局管道，但不开放默认应用路由。
// 已被映射隐藏的应用名、禁止应用和非法请求继续使用原来的拒绝结果。
func (host *nativeApplicationHost) resolveRequest(request *stdhttp.Request) (applicationResolution, bool, error) {
	resolution, err := host.resolver.resolve(request)
	if !errors.Is(err, errApplicationNotFound) || request == nil || request.URL == nil ||
		(request.Method != stdhttp.MethodGet && request.Method != stdhttp.MethodHead) {
		return resolution, false, err
	}
	segment, _ := firstApplicationPathSegment(request.URL.Path)
	if host.resolver.isReservedPublicPrefix(segment) {
		return resolution, false, err
	}
	return host.resolver.defaultResolution(request.URL.Path), true, nil
}

// isReservedPublicPrefix 只收紧静态适配边界，避免大小写不敏感的文件系统
// 把未知前缀重新映射到被禁止或隐藏的应用目录；普通应用解析仍保持原有规则。
func (resolver *applicationResolver) isReservedPublicPrefix(segment string) bool {
	for name := range resolver.denied {
		if strings.EqualFold(segment, name) {
			return true
		}
	}
	for alias, target := range resolver.appMap {
		if strings.EqualFold(segment, target) {
			return true
		}
		if _, denied := resolver.denied[target]; denied && strings.EqualFold(segment, alias) {
			return true
		}
	}
	return false
}

func (host *nativeApplicationHost) serveHTTP(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	if host == nil || host.resolver == nil || request == nil || request.URL == nil {
		stdhttp.Error(writer, stdhttp.StatusText(stdhttp.StatusBadRequest), stdhttp.StatusBadRequest)
		return
	}
	resolution, publicOnly, err := host.resolveRequest(request)
	if err != nil {
		writeApplicationResolutionError(writer, err)
		return
	}
	handler := host.applications[resolution.name]
	if handler == nil {
		writeApplicationResolutionError(writer, fmt.Errorf("%w: %q", errApplicationNotFound, resolution.name))
		return
	}
	handler.serveHTTP(writer, withResolvedApplication(request, resolution.requestContext(request.Host)), publicOnly)
}

// run 返回的布尔值表示响应结束阶段是否已由目标应用接管。
// 应用解析失败时仍由根内核保存请求状态，避免调用方作用域失去清理入口。
func (host *nativeApplicationHost) run(request *fwcontext.Request) (*fwcontext.Response, bool) {
	if host == nil || host.resolver == nil {
		return internalServerErrorResponse(), false
	}
	var raw *stdhttp.Request
	if request != nil {
		raw = request.Raw()
	}
	if raw == nil {
		var err error
		raw, err = stdhttp.NewRequest(stdhttp.MethodGet, "http://localhost/", nil)
		if err != nil {
			return internalServerErrorResponse(), false
		}
	}
	resolution, publicOnly, err := host.resolveRequest(raw)
	if err != nil {
		return responseForApplicationResolutionError(err), false
	}
	handler := host.applications[resolution.name]
	if handler == nil {
		return responseForApplicationResolutionError(errApplicationNotFound), false
	}
	if request == nil {
		request, err = handler.newDefaultRequest()
		if err != nil {
			handler.logHTTPError("创建目标应用默认请求失败", err)
			return internalServerErrorResponse(), false
		}
	} else {
		scope, scopeErr := handler.app.NewScope()
		if scopeErr != nil {
			handler.logHTTPError("创建目标应用请求作用域失败", scopeErr)
			return internalServerErrorResponse(), false
		}
		if bindErr := fwcontext.WithServiceScope(scope, scope)(request); bindErr != nil {
			handler.logHTTPError("绑定目标应用请求作用域失败", errors.Join(bindErr, scope.Close()))
			return internalServerErrorResponse(), false
		}
		request.WithEnv(handler.app.Env())
	}
	request.SetApplicationContext(resolution.requestContext(raw.Host))
	response := handler.run(request, publicOnly)
	if identity := response.Identity(); identity != nil {
		host.endMu.Lock()
		host.endHandlers[identity] = handler
		host.endMu.Unlock()
	}
	return response, true
}

func (host *nativeApplicationHost) endWithContext(
	ctx stdcontext.Context,
	response *fwcontext.Response,
) (bool, error) {
	handler := host.takeEndHandler(response)
	if handler == nil {
		return false, nil
	}
	return true, handler.endWithContext(ctx, response)
}

func (host *nativeApplicationHost) endAndReport(response *fwcontext.Response) bool {
	handler := host.takeEndHandler(response)
	if handler == nil {
		return false
	}
	handler.End(response)
	return true
}

func (host *nativeApplicationHost) takeEndHandler(response *fwcontext.Response) *Http {
	if host == nil || response == nil {
		return nil
	}
	identity := response.Identity()
	if identity == nil {
		return nil
	}
	host.endMu.Lock()
	handler := host.endHandlers[identity]
	delete(host.endHandlers, identity)
	host.endMu.Unlock()
	return handler
}

func writeApplicationResolutionError(writer stdhttp.ResponseWriter, err error) {
	response := responseForApplicationResolutionError(err)
	if sendErr := response.Send(writer); sendErr != nil {
		stdhttp.Error(writer, stdhttp.StatusText(stdhttp.StatusInternalServerError), stdhttp.StatusInternalServerError)
	}
}

func responseForApplicationResolutionError(err error) *fwcontext.Response {
	status := stdhttp.StatusInternalServerError
	switch {
	case errors.Is(err, errInvalidAppRequest):
		status = stdhttp.StatusBadRequest
	case errors.Is(err, errApplicationNotFound), errors.Is(err, errApplicationDenied):
		status = stdhttp.StatusNotFound
	}
	return fwcontext.NewResponse().Code(status).Content(stdhttp.StatusText(status) + "\n")
}
