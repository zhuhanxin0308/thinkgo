package framework

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

var (
	// ErrApplicationURL 表示应用 URL 无法安全生成。
	ErrApplicationURL = errors.New("应用 URL 生成失败")
)

// URLFor 生成当前请求所属应用的完整 URL；请求 Host 不会覆盖应用配置的域名。
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
	if request != nil {
		applicationPath, err := request.ApplicationPath(path)
		if err != nil {
			return ""
		}
		path = applicationPath
	}

	return app.URL(path)
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

func isAbsoluteHTTPURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")
}
