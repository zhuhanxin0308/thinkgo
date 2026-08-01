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

// ApplicationContext 保存一次请求选中的应用信息，创建后不再允许修改。
// 所有字段通过只读方法暴露，避免请求处理期间意外改变应用边界。
type ApplicationContext struct {
	name          string
	originalPath  string
	rewrittenPath string
	pathPrefix    string
	host          string
	canonicalHost string
	domainBound   bool
}

// NewApplicationContext 创建请求级应用上下文。
func NewApplicationContext(name, originalPath, rewrittenPath, pathPrefix, host string, domainBound bool) ApplicationContext {
	return NewApplicationContextWithCanonicalHost(name, originalPath, rewrittenPath, pathPrefix, host, host, domainBound)
}

// NewApplicationContextWithCanonicalHost 创建带可信 Host 的请求级应用上下文。
// canonicalHost 由应用解析器根据已配置的域名绑定产生，不直接采信外部请求 Host。
func NewApplicationContextWithCanonicalHost(name, originalPath, rewrittenPath, pathPrefix, host, canonicalHost string, domainBound bool) ApplicationContext {
	return ApplicationContext{
		name:          name,
		originalPath:  originalPath,
		rewrittenPath: rewrittenPath,
		pathPrefix:    strings.TrimSuffix(pathPrefix, "/"),
		host:          host,
		canonicalHost: canonicalHost,
		domainBound:   domainBound,
	}
}

// Name 返回当前应用名称。
func (application ApplicationContext) Name() string {
	return application.name
}

// OriginalPath 返回应用解析前的原始请求路径。
func (application ApplicationContext) OriginalPath() string {
	return application.originalPath
}

// RewrittenPath 返回交给应用处理器的路径。
func (application ApplicationContext) RewrittenPath() string {
	return application.rewrittenPath
}

// PathPrefix 返回路径应用被移除的 URL 前缀。
func (application ApplicationContext) PathPrefix() string {
	return application.pathPrefix
}

// Host 返回应用解析时使用的原始 Host。
func (application ApplicationContext) Host() string {
	return application.host
}

// CanonicalHost 返回用于生成绝对 URL 的可信 Host。
func (application ApplicationContext) CanonicalHost() string {
	if application.canonicalHost != "" {
		return application.canonicalHost
	}
	return application.host
}

// DomainBound 返回当前应用是否由域名绑定选中。
func (application ApplicationContext) DomainBound() bool {
	return application.domainBound
}

// applicationPath 将路由器生成的应用内路径转换为当前请求可访问的路径。
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

	parsed, err := url.ParseRequestURI(path)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: 路径必须是相对 HTTP 路径", ErrInvalidApplicationPath)
	}
	escapedPath := parsed.EscapedPath()
	if escapedPath == "" {
		escapedPath = "/"
	}
	if !strings.HasPrefix(escapedPath, "/") {
		path = "/" + path
		parsed, err = url.ParseRequestURI(path)
		if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" {
			return "", fmt.Errorf("%w: 路径必须是相对 HTTP 路径", ErrInvalidApplicationPath)
		}
		escapedPath = parsed.EscapedPath()
	}
	for _, marker := range []string{"%00", "%5c", "%2e%2e"} {
		if strings.Contains(strings.ToLower(escapedPath), marker) {
			return "", fmt.Errorf("%w: 路径包含禁止编码", ErrInvalidApplicationPath)
		}
	}

	prefix := application.pathPrefix
	if prefix == "" || escapedPath == prefix || strings.HasPrefix(escapedPath, prefix+"/") {
		return appendApplicationQuery(escapedPath, parsed), nil
	}
	if !strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "\\%\r\n") || strings.Contains(prefix, "..") {
		return "", fmt.Errorf("%w: 应用前缀非法", ErrInvalidApplicationPath)
	}
	return appendApplicationQuery(prefix+escapedPath, parsed), nil
}

// BuildApplicationPath 为指定应用前缀生成安全的站内路径。
func BuildApplicationPath(pathPrefix, path string) (string, error) {
	return NewApplicationContext("", "", "", pathPrefix, "", false).applicationPath(path)
}

func appendApplicationQuery(path string, parsed *url.URL) string {
	if parsed.RawQuery == "" {
		return path
	}
	return path + "?" + parsed.RawQuery
}
