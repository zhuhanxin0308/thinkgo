package context

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

var (
	// ErrInvalidApplicationPath 表示应用路径包含非法内容或无法安全拼接。
	ErrInvalidApplicationPath = errors.New("应用路径非法")
)

// ApplicationContext 保存一次请求选中的应用信息，创建后不再修改。
// 请求级上下文隔离 Go 并发请求，业务 API 仍保持 ThinkPHP 当前应用语义。
type ApplicationContext struct {
	name          string
	originalPath  string
	rewrittenPath string
	pathPrefix    string
	urlPrefix     string
	host          string
	canonicalHost string
	domainBound   bool
}

// NewApplicationContext 创建请求级应用上下文。
func NewApplicationContext(name, originalPath, rewrittenPath, pathPrefix, host string, domainBound bool) ApplicationContext {
	return NewApplicationContextWithCanonicalHost(name, originalPath, rewrittenPath, pathPrefix, host, host, domainBound)
}

// NewApplicationContextWithCanonicalHost 创建带可信 Host 的请求级应用上下文。
func NewApplicationContextWithCanonicalHost(name, originalPath, rewrittenPath, pathPrefix, host, canonicalHost string, domainBound bool) ApplicationContext {
	return NewApplicationContextWithURLPrefix(name, originalPath, rewrittenPath, pathPrefix, pathPrefix, host, canonicalHost, domainBound)
}

// NewApplicationContextWithURLPrefix 分离请求根路径与 URL 生成前缀。
// 默认应用从根地址进入时 Root 为空，但多应用 URL 仍可携带应用名称。
func NewApplicationContextWithURLPrefix(name, originalPath, rewrittenPath, pathPrefix, urlPrefix, host, canonicalHost string, domainBound bool) ApplicationContext {
	return ApplicationContext{
		name:          name,
		originalPath:  originalPath,
		rewrittenPath: rewrittenPath,
		pathPrefix:    strings.TrimSuffix(pathPrefix, "/"),
		urlPrefix:     strings.TrimSuffix(urlPrefix, "/"),
		host:          host,
		canonicalHost: canonicalHost,
		domainBound:   domainBound,
	}
}

// Name 返回当前应用名称。
func (application ApplicationContext) Name() string { return application.name }

// OriginalPath 返回应用解析前的原始请求路径。
func (application ApplicationContext) OriginalPath() string { return application.originalPath }

// RewrittenPath 返回交给当前应用路由器的路径。
func (application ApplicationContext) RewrittenPath() string { return application.rewrittenPath }

// PathPrefix 返回从请求 pathinfo 中移除的应用 URL 前缀。
func (application ApplicationContext) PathPrefix() string { return application.pathPrefix }

// URLPrefix 返回生成当前应用 URL 时使用的可见前缀。
func (application ApplicationContext) URLPrefix() string { return application.urlPrefix }

// Host 返回应用解析时收到的原始 Host。
func (application ApplicationContext) Host() string { return application.host }

// CanonicalHost 返回由绑定配置确认的可信 Host。
func (application ApplicationContext) CanonicalHost() string {
	if application.canonicalHost != "" {
		return application.canonicalHost
	}
	return application.host
}

// DomainBound 返回当前应用是否由域名或显式入口绑定。
func (application ApplicationContext) DomainBound() bool { return application.domainBound }

func (application ApplicationContext) applicationPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "/"
	}
	if strings.ContainsAny(path, "\r\n") {
		return "", fmt.Errorf("%w: 路径不能包含换行符", ErrInvalidApplicationPath)
	}
	for _, character := range path {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("%w: 路径不能包含控制字符", ErrInvalidApplicationPath)
		}
	}
	reference, err := url.Parse(path)
	if err != nil || reference.IsAbs() || reference.Host != "" || reference.Fragment != "" || strings.HasPrefix(path, "//") {
		return "", fmt.Errorf("%w: 路径必须是相对 HTTP 路径", ErrInvalidApplicationPath)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	parsed, err := url.ParseRequestURI(path)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: 路径必须是相对 HTTP 路径", ErrInvalidApplicationPath)
	}
	escapedPath := parsed.EscapedPath()
	if escapedPath == "" {
		escapedPath = "/"
	}
	if !strings.HasPrefix(escapedPath, "/") {
		escapedPath = "/" + escapedPath
	}
	for _, marker := range []string{"%00", "%5c", "%2e%2e"} {
		if strings.Contains(strings.ToLower(escapedPath), marker) {
			return "", fmt.Errorf("%w: 路径包含禁止编码", ErrInvalidApplicationPath)
		}
	}
	prefix := application.urlPrefix
	if prefix != "" && escapedPath != prefix && !strings.HasPrefix(escapedPath, prefix+"/") {
		if !validApplicationPathPrefix(prefix) {
			return "", fmt.Errorf("%w: 应用前缀非法", ErrInvalidApplicationPath)
		}
		escapedPath = prefix + escapedPath
	}
	if parsed.RawQuery != "" {
		escapedPath += "?" + parsed.RawQuery
	}
	return escapedPath, nil
}

func validApplicationPathPrefix(prefix string) bool {
	return strings.HasPrefix(prefix, "/") && !strings.ContainsAny(prefix, "\\%\r\n") && !strings.Contains(prefix, "..")
}

// BuildApplicationPath 为指定应用前缀生成安全站内路径。
func BuildApplicationPath(pathPrefix, path string) (string, error) {
	return NewApplicationContextWithURLPrefix("", "", "", pathPrefix, pathPrefix, "", "", false).applicationPath(path)
}
