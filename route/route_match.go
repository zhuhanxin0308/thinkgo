package route

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
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

	if matched, params := r.matchExplicit(method, requestParts, trailingSlash, host); matched != nil {
		return matched, params, nil
	}

	if matched := r.matchMissRoute(method, requestParts, host); matched != nil {
		return matched, nil, nil
	}

	if method == http.MethodOptions {
		if r.autoRoute {
			return automaticOptionsRoute(), nil, nil
		}
		return nil, nil, nil
	}

	if r.autoRoute && isSafeAutoRouteParts(requestParts) {
		if !r.removeSlash {
			// 严格斜杠不匹配仍保留显式路由对该路径的保护，避免绕到自动动作。
			if matched, _ := r.matchExplicit(method, requestParts, !trailingSlash, host); matched != nil {
				return nil, nil, nil
			}
		}
		// ThinkPHP 的 URL 自动调度不按请求方法限制控制器动作；鉴权和方法约束
		// 应由显式路由或中间件声明，未知控制器最终由 HTTP 分发层转换为 404。
		if automatic := r.resolveAutoRoute(normalizedPath, requestParts); automatic != nil {
			return automatic, nil, nil
		}
	}
	return nil, nil, nil
}

func (r *Router) matchExplicit(method string, parts []string, trailingSlash bool, host string) (*Route, map[string]string) {
	for _, candidateMethod := range [...]string{method, anyMethod} {
		if matched := r.matchStaticComplete(candidateMethod, parts, trailingSlash, host); matched != nil {
			return matched, nil
		}
		if matched, params := r.matchDynamic(candidateMethod, parts, trailingSlash, host, true); matched != nil {
			return matched, params
		}
		if !r.completeMatch {
			staticPrefix := r.matchStaticPrefixOnly(candidateMethod, parts, trailingSlash, host)
			dynamicPrefix, dynamicParams := r.matchDynamic(candidateMethod, parts, trailingSlash, host, false)
			if staticPrefix != nil && dynamicPrefix != nil {
				dynamicConsumed, _ := dynamicPrefix.matchedDynamicPathShape(dynamicParams)
				if dynamicConsumed > len(staticPrefix.pathParts) {
					return dynamicPrefix, dynamicParams
				}
				return staticPrefix, nil
			}
			if staticPrefix != nil {
				return staticPrefix, nil
			}
			if dynamicPrefix != nil {
				return dynamicPrefix, dynamicParams
			}
		}
	}
	return nil, nil
}

func (r *Router) matchMissRoute(method string, requestParts []string, host string) *Route {
	if r == nil {
		return nil
	}
	if len(r.missList) == 0 {
		if matched := r.missRoutes[method]; matched != nil {
			return matched
		}
		return r.missRoute
	}
	var best *Route
	for _, candidate := range r.missList {
		if candidate == nil || candidate.removed {
			continue
		}
		if candidate.method != method && candidate.method != anyMethod {
			continue
		}
		if candidate.domain != "" && candidate.domain != host {
			continue
		}
		parts := requestParts
		if candidate.extensionRequired {
			var ok bool
			parts, ok = stripRouteExtension(requestParts, candidate.ext, true)
			if !ok {
				continue
			}
		}
		if len(parts) < len(candidate.missParts) {
			continue
		}
		prefixMatched := true
		for index, part := range candidate.missParts {
			if !routeLiteralEqual(r, part.literal, parts[index]) {
				prefixMatched = false
				break
			}
		}
		if !prefixMatched {
			continue
		}
		if betterMissRoute(candidate, best, method) {
			best = candidate
		}
	}
	return best
}

func betterMissRoute(candidate, current *Route, method string) bool {
	if current == nil {
		return true
	}
	if len(candidate.missParts) != len(current.missParts) {
		return len(candidate.missParts) > len(current.missParts)
	}
	if (candidate.domain != "") != (current.domain != "") {
		return candidate.domain != ""
	}
	candidateExactMethod := candidate.method == method
	currentExactMethod := current.method == method
	if candidateExactMethod != currentExactMethod {
		return candidateExactMethod
	}
	return candidate.order >= current.order
}

func (r *Router) matchStaticComplete(method string, requestParts []string, trailingSlash bool, host string) *Route {
	methodRoutes := r.staticRoutes[method]
	if len(methodRoutes) == 0 {
		return nil
	}
	key := r.pathPartsKey(requestParts)
	var best *Route
	for _, candidate := range methodRoutes[key] {
		if candidate.domain != "" && candidate.domain != host {
			continue
		}
		if matched, _ := candidate.matchRequestParts(requestParts, trailingSlash, true); matched && moreSpecificStaticRoute(candidate, best, requestParts) {
			best = candidate
		}
	}
	if best != nil || len(requestParts) == 0 || !strings.HasPrefix(requestParts[len(requestParts)-1], ".") {
		return best
	}
	parentKey := ""
	if boundary := strings.LastIndexByte(key, 0); boundary >= 0 {
		parentKey = key[:boundary]
	}
	for _, candidate := range methodRoutes[parentKey] {
		if candidate.domain != "" && candidate.domain != host {
			continue
		}
		if matched, _ := candidate.matchRequestParts(requestParts, trailingSlash, true); matched && moreSpecificStaticRoute(candidate, best, requestParts) {
			best = candidate
		}
	}
	return best
}

func (r *Router) matchStaticPrefixOnly(method string, requestParts []string, trailingSlash bool, host string) *Route {
	methodRoutes := r.staticRoutes[method]
	if len(methodRoutes) == 0 || len(requestParts) == 0 {
		return nil
	}
	key := r.pathPartsKey(requestParts)
	for {
		if boundary := strings.LastIndexByte(key, 0); boundary >= 0 {
			key = key[:boundary]
		} else {
			key = ""
		}
		if best := matchStaticPrefix(methodRoutes[key], requestParts, trailingSlash, host); best != nil || key == "" {
			return best
		}
	}
}

func (r *Router) matchDynamic(method string, requestParts []string, trailingSlash bool, host string, requireComplete bool) (*Route, map[string]string) {
	if len(r.dynamicRoutes[method]) == 0 {
		return nil, nil
	}
	completeMatch := requireComplete || r.completeMatch
	for _, exactDomain := range []bool{true, false} {
		candidates := r.dynamicRoutes[method]
		if r.dynamicIndex != nil {
			candidates = r.dynamicIndex.candidatesWithOptions(method, host, requestParts, exactDomain, completeMatch)
		}
		var alias *Route
		var aliasParams map[string]string
		var aliasSpecificity int
		for _, candidate := range candidates {
			if alias != nil && routeSpecificity(candidate) < aliasSpecificity {
				break
			}
			if r.dynamicIndex == nil {
				if exactDomain {
					if candidate.domain == "" || candidate.domain != host {
						continue
					}
				} else if candidate.domain != "" {
					continue
				}
			}
			if matched, params := candidate.matchRequestParts(requestParts, trailingSlash, completeMatch); matched {
				usesAlias := candidate.matchesExtensionAlias(requestParts)
				if alias == nil && !usesAlias {
					return candidate, params
				}
				consumed, endsInLiteral := candidate.matchedDynamicPathShape(params)
				if alias == nil {
					aliasPartCount := len(requestParts)
					if requestParts[aliasPartCount-1] == "."+candidate.ext {
						aliasPartCount--
					}
					if consumed != aliasPartCount {
						return candidate, params
					}
					alias, aliasParams, aliasSpecificity = candidate, params, routeSpecificity(candidate)
				} else if !usesAlias && endsInLiteral && consumed == len(requestParts) {
					return candidate, params
				}
			}
		}
		if alias != nil {
			return alias, aliasParams
		}
	}
	return nil, nil
}

func matchStaticPrefix(candidates []*Route, requestParts []string, trailingSlash bool, host string) *Route {
	var best *Route
	bestExact := false
	for _, candidate := range candidates {
		if candidate.domain != "" && candidate.domain != host {
			continue
		}
		parts, ok := candidate.requestPartsForMatch(requestParts)
		if !ok {
			continue
		}
		exact := len(parts) == len(candidate.pathParts)
		if best != nil && (!exact && bestExact || exact == bestExact && !moreSpecificStaticRoute(candidate, best, requestParts)) {
			continue
		}
		if matched, _ := candidate.matchRequestPartsWithOptions(requestParts, trailingSlash); matched {
			best, bestExact = candidate, exact
		}
	}
	return best
}

// moreSpecificStaticRoute 依次比较路径深度、域名、字面后缀和注册顺序。
func moreSpecificStaticRoute(candidate, current *Route, requestParts []string) bool {
	if current == nil {
		return true
	}
	if len(candidate.pathParts) != len(current.pathParts) {
		return len(candidate.pathParts) > len(current.pathParts)
	}
	if (candidate.domain != "") != (current.domain != "") {
		return candidate.domain != ""
	}
	if len(candidate.pathParts) == len(requestParts) {
		if candidateAlias, currentAlias := candidate.matchesExtensionAlias(requestParts), current.matchesExtensionAlias(requestParts); candidateAlias != currentAlias {
			return !candidateAlias
		}
	}
	return candidate.order < current.order
}

func automaticOptionsRoute() *Route {
	const allow = "GET, POST, PUT, DELETE"
	return &Route{
		method: http.MethodOptions,
		path:   "__options__",
		handler: func(_ *context.Request) *context.Response {
			return context.NewResponse().Header("Allow", allow).NoContent()
		},
		patterns: make(map[string]string),
		compiled: make(map[string]*regexp.Regexp),
	}
}

// matchRequestPartsWithOptions 按路由器配置匹配请求路径，并保留参数原始大小写。
func (r *Route) matchRequestPartsWithOptions(requestParts []string, trailingSlash bool) (bool, map[string]string) {
	return r.matchRequestParts(requestParts, trailingSlash, false)
}

func (r *Route) matchRequestParts(requestParts []string, trailingSlash bool, requireComplete bool) (bool, map[string]string) {
	parts, ok := r.requestPartsForMatch(requestParts)
	if !ok {
		return false, nil
	}
	if r.router != nil && !r.router.removeSlash && r.trailingSlash != trailingSlash {
		return false, nil
	}
	completeMatch := requireComplete || r.jsonContract || r.router == nil || r.router.completeMatch
	if isStaticRouteParts(r.pathParts) {
		// 根路由没有可消费的路径段，只能匹配首页，不能成为所有路径的空前缀。
		if len(r.pathParts) == 0 || completeMatch {
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
		return r.acceptsLiteralRequestEnd(parts, len(r.pathParts)), nil
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
			if (!completeMatch || requestIndex == len(parts)) && r.acceptsLiteralRequestEnd(parts, requestIndex) {
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

// requestPathPartsWithOptions 解析请求路径，并按 removeSlash 决定是否保留末尾斜杠信息。
func requestPathPartsWithOptions(req *context.Request, removeSlash bool) ([]string, string, bool, error) {
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
	trailingSlash := !removeSlash && len(escapedPath) > 1 && strings.HasSuffix(escapedPath, "/")
	trimmed := strings.Trim(escapedPath, "/")
	if trimmed == "" {
		return []string{}, "/", false, nil
	}
	escapedParts := strings.Split(trimmed, "/")
	if len(escapedParts) > maxRouteSegments {
		return nil, "", false, fmt.Errorf("%w: %w: 路径段超过 %d", ErrInvalidRequestPath, ErrRouteTooComplex, maxRouteSegments)
	}
	parts := make([]string, 0, len(escapedParts))
	for _, escapedPart := range escapedParts {
		part, err := url.PathUnescape(escapedPart)
		if err != nil || !validDecodedRoutePathPart(part) {
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

func validDecodedRoutePathPart(part string) bool {
	if part == "" || part == "." || part == ".." {
		return false
	}
	for index := 0; index < len(part); index++ {
		char := part[index]
		if char < 0x20 || char == 0x7f || char == '/' || char == '\\' {
			return false
		}
	}
	return true
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
		return []string{"." + extension}
	}
	result[len(result)-1] += "." + extension
	return result
}

// hasLiteralExtension 判断声明路径的末段是否已经包含所用后缀。
func (r *Route) hasLiteralExtension(caseSensitive bool) bool {
	if r.ext == "" || len(r.pathParts) == 0 {
		return false
	}
	last := r.pathParts[len(r.pathParts)-1].literal
	ext := routePathExtension(last)
	if caseSensitive {
		return ext == r.ext
	}
	return strings.EqualFold(ext, r.ext)
}

// matchesExtensionAlias 只将实际剥离后缀的候选视为别名，字面候选之间保持注册顺序。
func (r *Route) matchesExtensionAlias(parts []string) bool {
	return r.ext != "" && !r.literalExtension && len(parts) > 0 && strings.HasSuffix(parts[len(parts)-1], "."+r.ext)
}

// matchedDynamicPathShape 忽略未消费的可选参数，计算真正参与匹配的请求尾段。
func (r *Route) matchedDynamicPathShape(params map[string]string) (consumed int, endsInLiteral bool) {
	consumed = len(params)
	for _, part := range r.pathParts {
		if part.param == "" {
			consumed++
			endsInLiteral = true
		} else if _, exists := params[part.param]; exists {
			endsInLiteral = false
		}
	}
	return consumed, endsInLiteral
}

func (r *Route) requestPartsForMatch(parts []string) ([]string, bool) {
	if r.literalExtension {
		return parts, true
	}
	return stripRouteExtension(parts, r.ext, r.extensionRequired)
}

// acceptsLiteralRequestEnd 仅对实际消费的字面后缀沿用路径的大小写策略。
func (r *Route) acceptsLiteralRequestEnd(parts []string, consumed int) bool {
	return !r.literalExtension || !r.extensionRequired || consumed == len(parts) ||
		routePathExtension(parts[len(parts)-1]) == r.ext
}

func routePathExtension(part string) string {
	if dot := strings.LastIndexByte(part, '.'); dot >= 0 {
		return part[dot+1:]
	}
	return ""
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
		normalized[index] = routeLiteralFoldKey(part)
	}
	return pathPartsKey(normalized)
}

// routeLiteralFoldKey 为完整的 Unicode 简单折叠等价类选择同一索引键。
// 静态和动态索引共用此规则，确保预筛选不会遗漏 EqualFold 可匹配的路由。
func routeLiteralFoldKey(part string) string {
	return strings.Map(func(value rune) rune {
		canonical := value
		for next := unicode.SimpleFold(value); next != value; next = unicode.SimpleFold(next) {
			if next < canonical {
				canonical = next
			}
		}
		if canonical >= 'A' && canonical <= 'Z' {
			canonical += 'a' - 'A'
		}
		return canonical
	}, part)
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
