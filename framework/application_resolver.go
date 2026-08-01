package framework

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

var (
	// ErrInvalidApplicationResolverConfig 表示应用解析配置不符合启动期安全约束。
	ErrInvalidApplicationResolverConfig = errors.New("应用解析配置非法")
	// ErrApplicationResolution 表示请求无法解析到可用应用。
	ErrApplicationResolution = errors.New("应用解析失败")
	// ErrApplicationNotFound 表示请求指定的应用不存在。
	ErrApplicationNotFound = errors.New("应用不存在")
	// ErrApplicationDenied 表示请求指定的应用被拒绝访问。
	ErrApplicationDenied = errors.New("应用访问被拒绝")
	// ErrInvalidApplicationRequest 表示请求 Host 或 URL 路径包含非法内容。
	ErrInvalidApplicationRequest = errors.New("应用请求非法")
)

// ApplicationResolution 保存一次请求的应用选择结果。
// RewrittenPath 只供请求副本使用，解析器不会修改传入的 http.Request。
type ApplicationResolution struct {
	Name          string
	OriginalPath  string
	RewrittenPath string
	PathPrefix    string
	DomainBound   bool
	CanonicalHost string
}

type applicationDomainBinding struct {
	pattern string
	target  string
}

// ApplicationResolver 持有只读的应用解析规则快照。
type ApplicationResolver struct {
	applications          map[string]struct{}
	defaultAppName        string
	appMap                map[string]string
	domainBindings        map[string]string
	subdomainBindings     map[string]string
	wildcardDomainBinding []applicationDomainBinding
	wildcardDomainTarget  string
	denyApplications      map[string]struct{}
	appExpress            bool
}

// NewApplicationResolver 从宿主默认应用配置构造解析器，并在启动阶段校验全部映射目标。
func NewApplicationResolver(manager *ApplicationManager) (*ApplicationResolver, error) {
	if manager == nil {
		return nil, ErrNilApplication
	}
	applications := manager.Applications()
	if len(applications) == 0 {
		return nil, ErrNoApplications
	}
	defaultApplication := manager.DefaultApplication()
	if defaultApplication == nil {
		return nil, fmt.Errorf("%w: 默认应用不存在", ErrInvalidApplicationResolverConfig)
	}

	resolver := &ApplicationResolver{
		applications:      make(map[string]struct{}, len(applications)),
		defaultAppName:    defaultApplication.ApplicationName,
		appMap:            make(map[string]string),
		domainBindings:    make(map[string]string),
		subdomainBindings: make(map[string]string),
		denyApplications:  make(map[string]struct{}),
	}
	for name := range applications {
		resolver.applications[name] = struct{}{}
	}
	configApp := defaultApplication
	if configuredDefault := applicationConfigString(configApp, "default_app"); configuredDefault != "" {
		resolver.defaultAppName = configuredDefault
	}
	if _, exists := resolver.applications[resolver.defaultAppName]; !exists {
		return nil, fmt.Errorf("%w: 默认应用 %q 未定义", ErrInvalidApplicationResolverConfig, resolver.defaultAppName)
	}

	denyApplications, err := parseDeniedApplications(applicationConfigValue(configApp, "deny_app_list"), resolver.applications)
	if err != nil {
		return nil, err
	}
	resolver.denyApplications = denyApplications
	if _, denied := resolver.denyApplications[resolver.defaultAppName]; denied {
		return nil, fmt.Errorf("%w: 默认应用 %q 不能位于拒绝列表", ErrInvalidApplicationResolverConfig, resolver.defaultAppName)
	}

	appMap, err := parseApplicationMap(applicationConfigValue(configApp, "app_map"), resolver.applications)
	if err != nil {
		return nil, err
	}
	resolver.appMap = appMap

	if err := resolver.parseDomainBindings(applicationConfigValue(configApp, "domain_bind")); err != nil {
		return nil, err
	}
	rawAppExpress := applicationConfigValue(configApp, "app_express")
	if resolver.appExpress, err = parseApplicationExpress(rawAppExpress); err != nil {
		return nil, err
	}
	// 只有一个应用且未显式配置 app_express 时，保留单应用直接访问任意业务路径的行为。
	if len(resolver.applications) == 1 && rawAppExpress == nil {
		resolver.appExpress = true
	}
	return resolver, nil
}

// Resolve 根据域名优先、路径其次的规则选择应用。
func (resolver *ApplicationResolver) Resolve(request *http.Request) (ApplicationResolution, error) {
	if resolver == nil || request == nil || request.URL == nil {
		return ApplicationResolution{}, fmt.Errorf("%w: 请求不能为空", ErrInvalidApplicationRequest)
	}
	originalPath, err := validateApplicationRequestPath(request.URL)
	if err != nil {
		return ApplicationResolution{}, err
	}
	host, err := normalizeApplicationHost(request.Host)
	if err != nil {
		return ApplicationResolution{}, err
	}
	if target, canonicalHost, matched := resolver.resolveDomain(host); matched {
		return resolver.domainResolution(target, originalPath, canonicalHost)
	}

	segment, hasSegment := firstApplicationPathSegment(originalPath)
	if !hasSegment {
		return resolver.defaultResolution(originalPath), nil
	}
	if _, denied := resolver.denyApplications[segment]; denied {
		return ApplicationResolution{}, fmt.Errorf("%w: %q", ErrApplicationDenied, segment)
	}

	target, known := resolver.appMap[segment]
	if !known {
		if _, exists := resolver.applications[segment]; exists {
			target = segment
			known = true
		} else if wildcardTarget := resolver.appMap["*"]; wildcardTarget != "" {
			target = wildcardTarget
			known = true
		}
	}
	if !known {
		if resolver.appExpress {
			return resolver.defaultResolution(originalPath), nil
		}
		return ApplicationResolution{}, fmt.Errorf("%w: %q", ErrApplicationNotFound, segment)
	}
	if _, denied := resolver.denyApplications[target]; denied {
		return ApplicationResolution{}, fmt.Errorf("%w: %q", ErrApplicationDenied, target)
	}
	if _, exists := resolver.applications[target]; !exists {
		return ApplicationResolution{}, fmt.Errorf("%w: %q", ErrApplicationNotFound, target)
	}
	return ApplicationResolution{
		Name:          target,
		OriginalPath:  originalPath,
		RewrittenPath: rewriteApplicationPath(originalPath, segment),
		PathPrefix:    "/" + segment,
	}, nil
}

func (resolver *ApplicationResolver) domainResolution(target, originalPath, canonicalHost string) (ApplicationResolution, error) {
	if _, denied := resolver.denyApplications[target]; denied {
		return ApplicationResolution{}, fmt.Errorf("%w: %q", ErrApplicationDenied, target)
	}
	if _, exists := resolver.applications[target]; !exists {
		return ApplicationResolution{}, fmt.Errorf("%w: %q", ErrApplicationNotFound, target)
	}
	return ApplicationResolution{
		Name:          target,
		OriginalPath:  originalPath,
		RewrittenPath: originalPath,
		DomainBound:   true,
		CanonicalHost: canonicalHost,
	}, nil
}

func (resolver *ApplicationResolver) defaultResolution(originalPath string) ApplicationResolution {
	return ApplicationResolution{
		Name:          resolver.defaultAppName,
		OriginalPath:  originalPath,
		RewrittenPath: originalPath,
	}
}

func (resolver *ApplicationResolver) resolveDomain(host string) (string, string, bool) {
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
	for _, binding := range resolver.wildcardDomainBinding {
		if strings.HasSuffix(host, binding.pattern) && host != strings.TrimPrefix(binding.pattern, ".") {
			return binding.target, host, true
		}
	}
	if resolver.wildcardDomainTarget != "" {
		return resolver.wildcardDomainTarget, host, true
	}
	return "", "", false
}

func (resolver *ApplicationResolver) parseDomainBindings(raw interface{}) error {
	values, err := applicationStringMap(raw, "domain_bind")
	if err != nil {
		return err
	}
	for pattern, target := range values {
		if target == "" {
			return fmt.Errorf("%w: domain_bind %q 的目标不能为空", ErrInvalidApplicationResolverConfig, pattern)
		}
		if _, exists := resolver.applications[target]; !exists {
			return fmt.Errorf("%w: domain_bind %q 指向未定义应用 %q", ErrInvalidApplicationResolverConfig, pattern, target)
		}
		if _, denied := resolver.denyApplications[target]; denied {
			return fmt.Errorf("%w: domain_bind %q 指向被拒绝应用 %q", ErrInvalidApplicationResolverConfig, pattern, target)
		}
		normalized, kind, normalizeErr := normalizeApplicationDomainPattern(pattern)
		if normalizeErr != nil {
			return fmt.Errorf("%w: %v", ErrInvalidApplicationResolverConfig, normalizeErr)
		}
		switch kind {
		case "all":
			if resolver.wildcardDomainTarget != "" {
				return fmt.Errorf("%w: domain_bind 通配规则重复", ErrInvalidApplicationResolverConfig)
			}
			resolver.wildcardDomainTarget = target
		case "subdomain":
			if _, exists := resolver.subdomainBindings[normalized]; exists {
				return fmt.Errorf("%w: domain_bind 子域名 %q 重复", ErrInvalidApplicationResolverConfig, pattern)
			}
			resolver.subdomainBindings[normalized] = target
		default:
			if _, exists := resolver.domainBindings[normalized]; exists {
				return fmt.Errorf("%w: domain_bind 域名 %q 重复", ErrInvalidApplicationResolverConfig, pattern)
			}
			resolver.domainBindings[normalized] = target
		}
		if kind == "wildcard" {
			resolver.wildcardDomainBinding = append(resolver.wildcardDomainBinding, applicationDomainBinding{pattern: normalized, target: target})
		}
	}
	sort.SliceStable(resolver.wildcardDomainBinding, func(left, right int) bool {
		return len(resolver.wildcardDomainBinding[left].pattern) > len(resolver.wildcardDomainBinding[right].pattern)
	})
	return nil
}

func applicationConfigValue(app *App, key string) interface{} {
	if app == nil || app.config == nil {
		return nil
	}
	if value := app.config.Get("app." + key); value != nil {
		return value
	}
	return app.config.Get(key)
}

func applicationConfigString(app *App, key string) string {
	value, _ := applicationConfigValue(app, key).(string)
	return strings.TrimSpace(value)
}

func parseApplicationMap(raw interface{}, applications map[string]struct{}) (map[string]string, error) {
	values, err := applicationStringMap(raw, "app_map")
	if err != nil {
		return nil, err
	}
	for alias, target := range values {
		if alias != "*" {
			if err := validateApplicationAlias(alias); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidApplicationResolverConfig, err)
			}
		}
		if _, exists := applications[target]; !exists {
			return nil, fmt.Errorf("%w: app_map %q 指向未定义应用 %q", ErrInvalidApplicationResolverConfig, alias, target)
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
		if err := validateApplicationAlias(name); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidApplicationResolverConfig, err)
		}
		if _, exists := applications[name]; !exists {
			return nil, fmt.Errorf("%w: deny_app_list 包含未定义应用 %q", ErrInvalidApplicationResolverConfig, name)
		}
		if _, exists := denied[name]; exists {
			return nil, fmt.Errorf("%w: deny_app_list 应用 %q 重复", ErrInvalidApplicationResolverConfig, name)
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
	return false, fmt.Errorf("%w: app_express 必须是布尔值", ErrInvalidApplicationResolverConfig)
}

func applicationStringMap(raw interface{}, field string) (map[string]string, error) {
	if raw == nil {
		return map[string]string{}, nil
	}
	result := make(map[string]string)
	switch values := raw.(type) {
	case map[string]interface{}:
		for key, value := range values {
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) != text {
				return nil, fmt.Errorf("%w: %s 的值必须是不含首尾空白的字符串", ErrInvalidApplicationResolverConfig, field)
			}
			result[key] = text
		}
	case map[string]string:
		for key, value := range values {
			result[key] = value
		}
	default:
		return nil, fmt.Errorf("%w: %s 必须是对象", ErrInvalidApplicationResolverConfig, field)
	}
	return result, nil
}

func applicationStringList(raw interface{}, field string) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	values := make([]string, 0)
	switch list := raw.(type) {
	case []interface{}:
		for _, value := range list {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("%w: %s 必须是字符串数组", ErrInvalidApplicationResolverConfig, field)
			}
			values = append(values, text)
		}
	case []string:
		values = append(values, list...)
	default:
		return nil, fmt.Errorf("%w: %s 必须是数组", ErrInvalidApplicationResolverConfig, field)
	}
	return values, nil
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
		suffix := strings.TrimPrefix(pattern, "*")
		if len(suffix) < 2 || strings.Contains(suffix[1:], "*") {
			return "", "", fmt.Errorf("通配域名绑定 %q 非法", pattern)
		}
		return suffix, "wildcard", nil
	}
	if strings.Contains(pattern, "*") {
		return "", "", fmt.Errorf("域名绑定 %q 的通配符位置非法", pattern)
	}
	if strings.Contains(pattern, ".") {
		host, err := normalizeApplicationHost(pattern)
		if err != nil {
			return "", "", err
		}
		return host, "exact", nil
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
		return "", fmt.Errorf("%w: Host 包含非法字符", ErrInvalidApplicationRequest)
	}
	if strings.HasPrefix(host, "[") {
		if parsedHost, _, err := net.SplitHostPort(host); err == nil {
			host = parsedHost
		} else if strings.HasSuffix(host, "]") {
			host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		} else {
			return "", fmt.Errorf("%w: Host 格式非法", ErrInvalidApplicationRequest)
		}
	} else if strings.Count(host, ":") == 1 {
		if parsedHost, _, err := net.SplitHostPort(host); err == nil {
			host = parsedHost
		}
	} else if strings.Count(host, ":") > 1 {
		return "", fmt.Errorf("%w: Host 格式非法", ErrInvalidApplicationRequest)
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return "", fmt.Errorf("%w: Host 不能为空", ErrInvalidApplicationRequest)
	}
	return host, nil
}

func validateApplicationRequestPath(requestURL *url.URL) (string, error) {
	escapedPath := strings.ToLower(requestURL.EscapedPath() + " " + requestURL.RawPath)
	for _, marker := range []string{"%2f", "%5c", "%00", "%2e%2e"} {
		if strings.Contains(escapedPath, marker) {
			return "", fmt.Errorf("%w: URL 路径包含禁止编码", ErrInvalidApplicationRequest)
		}
	}
	path := requestURL.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00\r\n") {
		return "", fmt.Errorf("%w: URL 路径格式非法", ErrInvalidApplicationRequest)
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
	if trimmed == "" || trimmed == "." || trimmed == ".." {
		return "", false
	}
	return trimmed, true
}

func rewriteApplicationPath(path, segment string) string {
	prefix := "/" + segment
	if path == prefix || path == prefix+"/" {
		return "/"
	}
	return strings.TrimPrefix(path, prefix)
}
