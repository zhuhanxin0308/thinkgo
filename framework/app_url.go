package framework

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	fwcontext "thinkgo/framework/context"
)

var (
	// ErrApplicationURL 表示应用 URL 无法安全生成。
	ErrApplicationURL = errors.New("应用 URL 生成失败")
)

// URLFor 根据请求级应用上下文生成当前应用的完整 URL。
// 路径应用会自动补回被解析器移除的前缀，域名绑定应用使用当前请求 Host。
func (app *App) URLFor(request *fwcontext.Request, path string) string {
	if app == nil {
		return ""
	}
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsAny(path, "\r\n\t") {
		return ""
	}
	if isAbsoluteHTTPURL(path) {
		return app.URL(path)
	}

	applicationPath := path
	if request != nil {
		var err error
		applicationPath, err = request.ApplicationPath(path)
		if err != nil {
			return ""
		}
		if application, ok := request.ApplicationContext(); ok && application.DomainBound() {
			// 域名应用只使用解析器确认过的 Host，拒绝把外部请求 Host 直接带入绝对 URL。
			if canonicalHost := application.CanonicalHost(); canonicalHost != "" {
				if built, err := buildApplicationHostURL(app, request, canonicalHost, applicationPath); err == nil {
					return built
				}
			}
			return ""
		}
	}
	return app.URL(applicationPath)
}

// AssetURLFor 根据请求级应用上下文生成静态资源 URL。
func (app *App) AssetURLFor(request *fwcontext.Request, path string) string {
	return app.URLFor(request, path)
}

// RouteURL 根据当前请求生成当前应用的命名路由完整 URL。
func (app *App) RouteURL(request *fwcontext.Request, name string, params map[string]interface{}) (string, error) {
	if app == nil || app.route == nil {
		return "", fmt.Errorf("%w: 应用路由器不可用", ErrApplicationURL)
	}
	path, err := app.route.URL(name, params)
	if err != nil {
		return "", err
	}
	builtURL := app.URLFor(request, path)
	if builtURL == "" {
		return "", fmt.Errorf("%w: 路由 %q 的路径非法", ErrApplicationURL, name)
	}
	return builtURL, nil
}

// URLForApplication 生成指定应用的完整 URL。
// 目标应用存在精确域名绑定时使用域名，否则使用应用映射别名或应用名称作为路径前缀。
func (app *App) URLForApplication(request *fwcontext.Request, targetApplication, path string) (string, error) {
	if app == nil {
		return "", fmt.Errorf("%w: 当前应用不可用", ErrApplicationURL)
	}
	if strings.TrimSpace(targetApplication) == "" || targetApplication == app.ApplicationName {
		builtURL := app.URLFor(request, path)
		if builtURL == "" {
			return "", fmt.Errorf("%w: 当前应用路径非法", ErrApplicationURL)
		}
		return builtURL, nil
	}
	if app.applicationManager == nil {
		return "", fmt.Errorf("%w: 当前应用未连接应用管理器", ErrApplicationURL)
	}
	return app.applicationManager.ApplicationURL(request, targetApplication, path)
}

// RouteURLForApplication 生成指定应用的命名路由完整 URL。
func (app *App) RouteURLForApplication(request *fwcontext.Request, targetApplication, name string, params map[string]interface{}) (string, error) {
	if app == nil {
		return "", fmt.Errorf("%w: 当前应用不可用", ErrApplicationURL)
	}
	if strings.TrimSpace(targetApplication) == "" || targetApplication == app.ApplicationName {
		return app.RouteURL(request, name, params)
	}
	if app.applicationManager == nil {
		return "", fmt.Errorf("%w: 当前应用未连接应用管理器", ErrApplicationURL)
	}
	target, exists := app.applicationManager.Application(targetApplication)
	if !exists || target == nil || target.route == nil {
		return "", fmt.Errorf("%w: 目标应用 %q 不存在", ErrApplicationNotFound, targetApplication)
	}
	path, err := target.route.URL(name, params)
	if err != nil {
		return "", err
	}
	return app.applicationManager.ApplicationURL(request, targetApplication, path)
}

// ApplicationURL 为指定应用生成完整 URL。
func (manager *ApplicationManager) ApplicationURL(request *fwcontext.Request, targetApplication, path string) (string, error) {
	if manager == nil {
		return "", ErrNilApplication
	}
	targetApplication = strings.TrimSpace(targetApplication)
	if targetApplication == "" {
		targetApplication = manager.defaultAppName
	}
	target, exists := manager.Application(targetApplication)
	if !exists || target == nil {
		return "", fmt.Errorf("%w: 目标应用 %q 不存在", ErrApplicationNotFound, targetApplication)
	}
	if request != nil {
		if application, hasApplication := request.ApplicationContext(); hasApplication && application.Name() == targetApplication {
			builtURL := target.URLFor(request, path)
			if builtURL == "" {
				return "", fmt.Errorf("%w: 目标应用路径非法", ErrApplicationURL)
			}
			return builtURL, nil
		}
	}

	resolver, err := manager.applicationURLResolver()
	if err != nil {
		return "", err
	}
	prefix, host, err := resolver.applicationURLScope(targetApplication)
	if err != nil {
		return "", err
	}
	applicationPath, err := fwcontext.BuildApplicationPath(prefix, path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrApplicationURL, err)
	}
	if host == "" {
		builtURL := target.URL(applicationPath)
		if builtURL == "" {
			return "", fmt.Errorf("%w: 目标应用 URL 无效", ErrApplicationURL)
		}
		return builtURL, nil
	}
	return buildApplicationHostURL(target, request, host, applicationPath)
}

func (resolver *ApplicationResolver) applicationURLScope(targetApplication string) (string, string, error) {
	if resolver == nil {
		return "", "", ErrInvalidApplicationResolverConfig
	}
	if _, exists := resolver.applications[targetApplication]; !exists {
		return "", "", fmt.Errorf("%w: 目标应用 %q 不存在", ErrApplicationNotFound, targetApplication)
	}
	if _, denied := resolver.denyApplications[targetApplication]; denied {
		return "", "", fmt.Errorf("%w: 目标应用 %q 被拒绝访问", ErrApplicationDenied, targetApplication)
	}

	domains := make([]string, 0)
	for domain, target := range resolver.domainBindings {
		if target == targetApplication {
			domains = append(domains, domain)
		}
	}
	if len(domains) > 0 {
		sort.Strings(domains)
		return "", domains[0], nil
	}
	if targetApplication == resolver.defaultAppName {
		return "", "", nil
	}

	aliases := make([]string, 0)
	for alias, target := range resolver.appMap {
		if alias != "*" && target == targetApplication {
			aliases = append(aliases, alias)
		}
	}
	sort.Slice(aliases, func(left, right int) bool {
		if len(aliases[left]) != len(aliases[right]) {
			return len(aliases[left]) < len(aliases[right])
		}
		return aliases[left] < aliases[right]
	})
	if len(aliases) > 0 {
		return "/" + aliases[0], "", nil
	}
	return "/" + targetApplication, "", nil
}

func buildApplicationHostURL(app *App, request *fwcontext.Request, host, path string) (string, error) {
	if app == nil || strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("%w: 目标 Host 不可用", ErrApplicationURL)
	}
	scheme := "https"
	if request != nil {
		scheme = request.Scheme()
	} else if domain, err := url.Parse(app.Domain()); err == nil && domain.Scheme != "" {
		scheme = domain.Scheme
	}
	relative, err := url.ParseRequestURI(path)
	if err != nil || relative.Host != "" || relative.IsAbs() {
		return "", fmt.Errorf("%w: 目标应用路径无效", ErrApplicationURL)
	}
	absolute := &url.URL{Scheme: scheme, Host: host, Path: relative.Path, RawPath: relative.RawPath, RawQuery: relative.RawQuery}
	return absolute.String(), nil
}

func isAbsoluteHTTPURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")
}
