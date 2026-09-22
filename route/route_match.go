package route

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// Match 在冻结后的只读索引中匹配请求，并区分未找到、方法不允许和非法请求路径。
func (r *Router) Match(req *context.Request) (*Route, map[string]string, error) {
	if req == nil {
		return nil, nil, ErrNilRequest
	}
	if err := r.Freeze(); err != nil {
		return nil, nil, err
	}
	requestParts, normalizedPath, trailingSlash, err := requestPathPartsWithOptions(req, r.removeSlash)
	if err != nil {
		return nil, nil, err
	}
	method, err := normalizeRouteMethod(req.Method())
	if err != nil || method == anyMethod {
		return nil, nil, fmt.Errorf("%w: 请求方法 %q 非法", ErrInvalidRoute, req.Method())
	}
	host := normalizeRequestHost(req.Host())

	methods := []string{method}
	methods = append(methods, anyMethod)
	for _, candidateMethod := range methods {
		if matched := r.matchStatic(candidateMethod, requestParts, trailingSlash, host); matched != nil {
			return matched, nil, nil
		}
		if matched, params := r.matchDynamic(candidateMethod, requestParts, trailingSlash, host); matched != nil {
			return matched, params, nil
		}
	}

	if matched := r.matchMissRoute(method); matched != nil {
		return matched, nil, nil
	}

	if method == http.MethodOptions {
		if r.autoRoute {
			return automaticOptionsRoute(), nil, nil
		}
		return nil, nil, nil
	}

	if r.autoRoute && isSafeAutoRouteParts(requestParts) {
		// ThinkPHP 的 URL 自动调度不按请求方法限制控制器动作；鉴权和方法约束
		// 应由显式路由或中间件声明，未知控制器最终由 HTTP 分发层转换为 404。
		if automatic := r.resolveAutoRoute(normalizedPath, requestParts); automatic != nil {
			return automatic, nil, nil
		}
	}
	return nil, nil, nil
}

func (r *Router) matchMissRoute(method string) *Route {
	if r == nil {
		return nil
	}
	if matched := r.missRoutes[method]; matched != nil {
		return matched
	}
	return r.missRoute
}

func (r *Router) matchStatic(method string, requestParts []string, trailingSlash bool, host string) *Route {
	methodRoutes := r.staticRoutes[method]
	if len(methodRoutes) == 0 {
		return nil
	}
	candidates := r.staticCandidates(method, methodRoutes, requestParts)
	for _, exactDomain := range []bool{true, false} {
		if matched := candidates.firstMatch(host, requestParts, trailingSlash, exactDomain); matched != nil {
			return matched
		}
	}
	return nil
}

func (r *Router) matchDynamic(method string, requestParts []string, trailingSlash bool, host string) (*Route, map[string]string) {
	for _, exactDomain := range []bool{true, false} {
		candidates := r.dynamicRoutes[method]
		if r.dynamicIndex != nil {
			candidates = r.dynamicIndex.candidates(method, host, requestParts, exactDomain)
		}
		for _, candidate := range candidates {
			if r.dynamicIndex == nil {
				if exactDomain {
					if candidate.domain == "" || candidate.domain != host {
						continue
					}
				} else if candidate.domain != "" {
					continue
				}
			}
			if matched, params := candidate.matchRequestPartsWithOptions(requestParts, trailingSlash); matched {
				return candidate, params
			}
		}
	}
	return nil, nil
}

// staticCandidateSegments 将精确索引与冻结注册表分段保存；前缀段按需只读扫描，不复制共享切片。
type staticCandidateSegments struct {
	indexed          []*Route
	registered       []*Route
	method           string
	requestPartCount int
	includePrefix    bool
}

func (r *Router) staticCandidates(method string, indexedPaths map[string][]*Route, requestParts []string) staticCandidateSegments {
	return staticCandidateSegments{
		indexed:          indexedPaths[r.pathPartsKey(requestParts)],
		registered:       r.routes,
		method:           method,
		requestPartCount: len(requestParts),
		includePrefix:    !r.completeMatch,
	}
}

// firstMatch 保持原有候选优先级：先精确路径索引，再注册顺序前缀；域名精确匹配整体优先于公共路由。
func (candidates staticCandidateSegments) firstMatch(host string, requestParts []string, trailingSlash, exactDomain bool) *Route {
	if matched := firstMatchingStaticRoute(candidates.indexed, host, requestParts, trailingSlash, exactDomain); matched != nil {
		return matched
	}
	if !candidates.includePrefix {
		return nil
	}
	for _, candidate := range candidates.registered {
		if !isStaticPrefixCandidate(candidate, candidates.method, candidates.requestPartCount) {
			continue
		}
		if exactDomain {
			if candidate.domain == "" || candidate.domain != host {
				continue
			}
		} else if candidate.domain != "" {
			continue
		}
		if matched, _ := candidate.matchRequestPartsWithOptions(requestParts, trailingSlash); matched {
			return candidate
		}
	}
	return nil
}

func firstMatchingStaticRoute(candidates []*Route, host string, requestParts []string, trailingSlash, exactDomain bool) *Route {
	for _, candidate := range candidates {
		if exactDomain {
			if candidate.domain == "" || candidate.domain != host {
				continue
			}
		} else if candidate.domain != "" {
			continue
		}
		if matched, _ := candidate.matchRequestPartsWithOptions(requestParts, trailingSlash); matched {
			return candidate
		}
	}
	return nil
}

func isStaticPrefixCandidate(candidate *Route, method string, requestPartCount int) bool {
	return candidate != nil && candidate.method == method && isStaticRouteParts(candidate.pathParts) && len(candidate.pathParts) <= requestPartCount
}

func automaticOptionsRoute() *Route {
	const allow = "GET, POST, PUT, DELETE"
	return &Route{
		method: http.MethodOptions,
		path:   "__options__",
		handler: func(_ *context.Request) *context.Response {
			return context.NewResponse().Header("Allow", allow).NoContent()
		},
		middlewares: []middleware.Handler{},
		patterns:    make(map[string]string),
		compiled:    make(map[string]*regexp.Regexp),
	}
}

// matchRequestPartsWithOptions 按路由器配置匹配请求路径，并保留参数原始大小写。
func (r *Route) matchRequestPartsWithOptions(requestParts []string, trailingSlash bool) (bool, map[string]string) {
	parts, ok := stripRouteExtension(requestParts, r.ext, r.extensionRequired)
	if !ok {
		return false, nil
	}
	if r.router != nil && !r.router.removeSlash && r.trailingSlash != trailingSlash {
		return false, nil
	}
	if isStaticRouteParts(r.pathParts) {
		// 根路由没有可消费的路径段，只能匹配首页，不能成为所有路径的空前缀。
		if len(r.pathParts) == 0 || r.jsonContract || r.router == nil || r.router.completeMatch {
			if len(parts) != len(r.pathParts) {
				return false, nil
			}
		} else if len(parts) < len(r.pathParts) {
			return false, nil
		}
		for index, part := range r.pathParts {
			if !routeLiteralEqual(r.router, part.literal, parts[index]) {
				return false, nil
			}
		}
		return true, nil
	}

	type state struct{ routeIndex, requestIndex int }
	failed := make(map[state]bool)
	var visit func(int, int) (map[string]string, bool)
	visit = func(routeIndex, requestIndex int) (map[string]string, bool) {
		current := state{routeIndex: routeIndex, requestIndex: requestIndex}
		if failed[current] {
			return nil, false
		}
		if routeIndex == len(r.pathParts) {
			completeMatch := r.jsonContract || r.router == nil || r.router.completeMatch
			if !completeMatch || requestIndex == len(parts) {
				return make(map[string]string), true
			}
			failed[current] = true
			return nil, false
		}

		part := r.pathParts[routeIndex]
		if part.param == "" {
			if requestIndex < len(parts) && routeLiteralEqual(r.router, part.literal, parts[requestIndex]) {
				if params, matched := visit(routeIndex+1, requestIndex+1); matched {
					return params, true
				}
			}
			failed[current] = true
			return nil, false
		}

		if requestIndex < len(parts) && r.validateParam(part.param, parts[requestIndex]) {
			if params, matched := visit(routeIndex+1, requestIndex+1); matched {
				params[part.param] = parts[requestIndex]
				return params, true
			}
		}
		if part.optional {
			if params, matched := visit(routeIndex+1, requestIndex); matched {
				// 省略的可选变量不写入参数集合，使控制器动作能够使用自身默认值。
				return params, true
			}
		}
		failed[current] = true
		return nil, false
	}

	params, matched := visit(0, 0)
	if !matched || len(params) == 0 {
		return matched, nil
	}
	return true, params
}

func (r *Route) validateParam(name, value string) bool {
	if compiled := r.compiled[name]; compiled != nil {
		return compiled.MatchString(value)
	}
	if r.router != nil && r.router.compiledDefaultPattern != nil {
		return r.router.compiledDefaultPattern.MatchString(value)
	}
	return value != ""
}

func requestPathParts(req *context.Request) ([]string, string, error) {
	raw := req.Raw()
	if raw == nil || raw.URL == nil {
		return nil, "", fmt.Errorf("%w: URL 为空", ErrInvalidRequestPath)
	}
	escapedPath := raw.URL.EscapedPath()
	if escapedPath == "" {
		escapedPath = "/"
	}
	if len(escapedPath) > maxRoutePathLength || !strings.HasPrefix(escapedPath, "/") || strings.Contains(escapedPath, "\\") {
		return nil, "", fmt.Errorf("%w: %q", ErrInvalidRequestPath, escapedPath)
	}
	if strings.Contains(escapedPath, "//") {
		return nil, "", fmt.Errorf("%w: 路径包含空段", ErrInvalidRequestPath)
	}
	if len(escapedPath) > 1 {
		escapedPath = strings.TrimSuffix(escapedPath, "/")
	}
	trimmed := strings.Trim(escapedPath, "/")
	if trimmed == "" {
		return []string{}, "/", nil
	}
	escapedParts := strings.Split(trimmed, "/")
	if len(escapedParts) > maxRouteSegments {
		return nil, "", fmt.Errorf("%w: %v", ErrRouteTooComplex, escapedPath)
	}
	parts := make([]string, 0, len(escapedParts))
	for _, escapedPart := range escapedParts {
		part, err := url.PathUnescape(escapedPart)
		if err != nil || part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n") {
			return nil, "", fmt.Errorf("%w: 路径段 %q 非法", ErrInvalidRequestPath, escapedPart)
		}
		parts = append(parts, part)
	}
	return parts, "/" + strings.Join(parts, "/"), nil
}

// requestPathPartsWithOptions 解析请求路径，并按 removeSlash 决定是否保留末尾斜杠信息。
func requestPathPartsWithOptions(req *context.Request, removeSlash bool) ([]string, string, bool, error) {
	if removeSlash {
		parts, normalized, err := requestPathParts(req)
		return parts, normalized, false, err
	}
	raw := req.Raw()
	if raw == nil || raw.URL == nil {
		return nil, "", false, fmt.Errorf("%w: URL 为空", ErrInvalidRequestPath)
	}
	escapedPath := raw.URL.EscapedPath()
	if escapedPath == "" {
		escapedPath = "/"
	}
	if len(escapedPath) > maxRoutePathLength || !strings.HasPrefix(escapedPath, "/") || strings.Contains(escapedPath, "\\") {
		return nil, "", false, fmt.Errorf("%w: %q", ErrInvalidRequestPath, escapedPath)
	}
	if strings.Contains(escapedPath, "//") {
		return nil, "", false, fmt.Errorf("%w: 路径包含空段", ErrInvalidRequestPath)
	}
	trailingSlash := len(escapedPath) > 1 && strings.HasSuffix(escapedPath, "/")
	trimmed := strings.Trim(escapedPath, "/")
	if trimmed == "" {
		return []string{}, "/", false, nil
	}
	escapedParts := strings.Split(trimmed, "/")
	if len(escapedParts) > maxRouteSegments {
		return nil, "", false, fmt.Errorf("%w: %v", ErrRouteTooComplex, escapedPath)
	}
	parts := make([]string, 0, len(escapedParts))
	for _, escapedPart := range escapedParts {
		part, err := url.PathUnescape(escapedPart)
		if err != nil || part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n") {
			return nil, "", false, fmt.Errorf("%w: 路径段 %q 非法", ErrInvalidRequestPath, escapedPart)
		}
		parts = append(parts, part)
	}
	normalized := "/" + strings.Join(parts, "/")
	if trailingSlash {
		normalized += "/"
	}
	return parts, normalized, trailingSlash, nil
}

func canonicalRoutePath(rawPath string) (string, int, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	_, optionalCount, err := parseRoutePath(path)
	return path, optionalCount, err
}

func parseRoutePath(path string) ([]routePart, int, error) {
	if len(path) > maxRoutePathLength || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "#\\\x00\r\n") || strings.Contains(path, "//") {
		return nil, 0, fmt.Errorf("%w: 路径 %q 非法", ErrInvalidRoute, path)
	}
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return []routePart{}, 0, nil
	}
	rawParts := strings.Split(trimmed, "/")
	if len(rawParts) > maxRouteSegments {
		return nil, 0, fmt.Errorf("%w: 路径段超过 %d", ErrRouteTooComplex, maxRouteSegments)
	}
	parts := make([]routePart, 0, len(rawParts))
	paramNames := make(map[string]bool)
	optionalCount := 0
	for _, rawPart := range rawParts {
		if rawPart == "." || rawPart == ".." {
			return nil, 0, fmt.Errorf("%w: 路径包含点段", ErrInvalidRoute)
		}
		if !strings.HasPrefix(rawPart, ":") {
			if strings.Contains(rawPart, "?") {
				return nil, 0, fmt.Errorf("%w: 静态路径段 %q 非法", ErrInvalidRoute, rawPart)
			}
			parts = append(parts, routePart{literal: rawPart})
			continue
		}
		name := strings.TrimPrefix(rawPart, ":")
		optional := strings.HasSuffix(name, "?")
		name = strings.TrimSuffix(name, "?")
		if !isValidAutoRouteSegment(name) || strings.Contains(name, ":") {
			return nil, 0, fmt.Errorf("%w: 参数段 %q 非法", ErrInvalidRoute, rawPart)
		}
		if paramNames[name] {
			return nil, 0, fmt.Errorf("%w: 参数 %q 重复", ErrInvalidRoute, name)
		}
		paramNames[name] = true
		if optional {
			optionalCount++
			if optionalCount > maxOptionalRouteSegments {
				return nil, 0, fmt.Errorf("%w: 可选段超过 %d", ErrRouteTooComplex, maxOptionalRouteSegments)
			}
		}
		parts = append(parts, routePart{param: name, optional: optional})
	}
	return parts, optionalCount, nil
}

func joinRoutePath(prefix, child string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	child = strings.Trim(strings.TrimSpace(child), "/")
	switch {
	case prefix == "" && child == "":
		return "/"
	case prefix == "":
		return "/" + child
	case child == "":
		return "/" + prefix
	default:
		return "/" + prefix + "/" + child
	}
}

func isStaticRouteParts(parts []routePart) bool {
	for _, part := range parts {
		if part.param != "" {
			return false
		}
	}
	return true
}

func routePartsWithExtension(parts []routePart, extension string) []string {
	result := make([]string, len(parts))
	for index, part := range parts {
		result[index] = part.literal
	}
	if extension == "" {
		return result
	}
	if len(result) == 0 {
		return result
	}
	result[len(result)-1] += "." + extension
	return result
}

func stripRouteExtension(parts []string, extension string, required bool) ([]string, bool) {
	if extension == "" {
		return parts, true
	}
	if len(parts) == 0 {
		// 根路由没有可承载后缀的路径段；全局默认后缀是可选项，
		// 因此只有显式 WithExtension 时才拒绝无后缀根路径。
		return parts, !required
	}
	suffix := "." + extension
	last := parts[len(parts)-1]
	if !strings.HasSuffix(last, suffix) {
		if !required {
			return parts, true
		}
		return nil, false
	}
	result := append([]string(nil), parts...)
	base := strings.TrimSuffix(last, suffix)
	if base == "" {
		result = result[:len(result)-1]
	} else {
		result[len(result)-1] = base
	}
	return result, true
}

func pathPartsKey(parts []string) string {
	return strings.Join(parts, "\x00")
}

// pathPartsKey 按路由器大小写配置生成索引键。
func (r *Router) pathPartsKey(parts []string) string {
	if r == nil || r.caseSensitive {
		return pathPartsKey(parts)
	}
	normalized := make([]string, len(parts))
	for index, part := range parts {
		normalized[index] = strings.ToLower(part)
	}
	return pathPartsKey(normalized)
}

// routeLiteralEqual 按路由器大小写配置比较静态路径段。
func routeLiteralEqual(router *Router, expected, actual string) bool {
	if router == nil || router.caseSensitive {
		return expected == actual
	}
	return strings.EqualFold(expected, actual)
}

// routeHasTrailingSlash 判断路由定义是否显式保留末尾斜杠。
func routeHasTrailingSlash(path string) bool {
	trimmed := strings.TrimSpace(path)
	return len(trimmed) > 1 && strings.HasSuffix(trimmed, "/")
}

func routeSpecificity(route *Route) int {
	score := 0
	for _, part := range route.pathParts {
		switch {
		case part.param == "":
			score += 100
		case part.optional:
			score += 1
		default:
			score += 10
		}
		if part.param != "" && route.compiled[part.param] != nil {
			score += 5
		}
	}
	return score
}

func normalizeRouteExtension(extension string) (string, error) {
	extension = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(extension), "."))
	if extension == "" || len(extension) > 32 {
		return "", fmt.Errorf("%w: 扩展名 %q 非法", ErrInvalidRoute, extension)
	}
	for _, char := range extension {
		if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '-' && char != '_' {
			return "", fmt.Errorf("%w: 扩展名 %q 非法", ErrInvalidRoute, extension)
		}
	}
	return extension, nil
}

// normalizeOptionalRouteExtension 校验允许为空的全局 URL 后缀。
func normalizeOptionalRouteExtension(extension string) (string, error) {
	if strings.TrimSpace(extension) == "" {
		return "", nil
	}
	return normalizeRouteExtension(extension)
}

func normalizeAndValidateRouteDomain(rawDomain string) (string, error) {
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(rawDomain), "."))
	if domain == "" {
		return "", nil
	}
	if len(domain) > 253 || strings.ContainsAny(domain, "/\\@?#\x00\r\n\t ") {
		return "", fmt.Errorf("%w: %q", ErrInvalidRouteDomain, rawDomain)
	}
	if strings.HasPrefix(domain, "[") && strings.HasSuffix(domain, "]") {
		domain = strings.Trim(domain, "[]")
	}
	if net.ParseIP(domain) != nil {
		return domain, nil
	}
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("%w: %q", ErrInvalidRouteDomain, rawDomain)
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '-' {
				return "", fmt.Errorf("%w: %q", ErrInvalidRouteDomain, rawDomain)
			}
		}
	}
	return domain, nil
}

func normalizeRequestHost(rawHost string) string {
	host := strings.TrimSpace(rawHost)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
}

func isSafeAutoRouteParts(parts []string) bool {
	for _, part := range parts {
		if strings.Contains(part, "/") || !isValidAutoRouteSegment(part) {
			return false
		}
	}
	return true
}

func (r *Router) resolveAutoRoute(path string, parts []string) *Route {
	controller := r.defaultController
	action := r.defaultAction
	handler := strings.ReplaceAll(controller, ".", "/") + "/" + action
	if len(parts) == 1 {
		controller = ucfirst(parts[0])
		handler = parts[0] + "/" + action
	} else if len(parts) > 1 {
		controllerParts := make([]string, 0, len(parts)-1)
		for _, part := range parts[:len(parts)-1] {
			controllerParts = append(controllerParts, ucfirst(part))
		}
		controller = strings.Join(controllerParts, ".")
		action = parts[len(parts)-1]
		handler = strings.Join(parts, "/")
	}
	if !isValidAutoControllerName(controller) || !isValidAutoRouteSegment(action) {
		return nil
	}
	return &Route{
		method:          anyMethod,
		path:            path,
		handler:         handler,
		auto:            true,
		controllerLayer: r.controllerLayer,
		middlewares:     []middleware.Handler{},
		patterns:        make(map[string]string),
		compiled:        make(map[string]*regexp.Regexp),
	}
}

func isValidAutoControllerName(name string) bool {
	parts := strings.Split(name, ".")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !isValidAutoRouteSegment(part) {
			return false
		}
	}
	return true
}

func isValidAutoRouteSegment(segment string) bool {
	if segment == "" {
		return false
	}
	for index := 0; index < len(segment); index++ {
		char := segment[index]
		letter := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
		digit := char >= '0' && char <= '9'
		if index == 0 && !letter && char != '_' {
			return false
		}
		if index > 0 && !letter && !digit && char != '_' {
			return false
		}
	}
	return true
}

func ucfirst(value string) string {
	if value == "" {
		return value
	}
	runes := []rune(value)
	if runes[0] >= 'a' && runes[0] <= 'z' {
		runes[0] -= 'a' - 'A'
	}
	return string(runes)
}
