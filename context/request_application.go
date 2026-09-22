package context

import (
	"fmt"
	"net/url"
	"strings"
)

// SetApplicationContext 写入当前请求的应用上下文。
// HTTP 内核在进入全局中间件前只设置一次，读取方始终获得值拷贝。
func (r *Request) SetApplicationContext(application ApplicationContext) {
	if r == nil {
		return
	}
	applicationCopy := application
	r.applicationMu.Lock()
	r.application = &applicationCopy
	r.applicationMu.Unlock()
}

// ApplicationContext 返回当前请求的应用上下文快照。
func (r *Request) ApplicationContext() (ApplicationContext, bool) {
	if r == nil {
		return ApplicationContext{}, false
	}
	r.applicationMu.RLock()
	if r.application == nil {
		r.applicationMu.RUnlock()
		return ApplicationContext{}, false
	}
	application := *r.application
	r.applicationMu.RUnlock()
	return application, true
}

// ApplicationPath 为当前请求生成包含应用映射前缀的站内路径。
func (r *Request) ApplicationPath(path string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("%w: 请求为空", ErrInvalidApplicationPath)
	}
	application, exists := r.ApplicationContext()
	if !exists {
		return BuildApplicationPath("", path)
	}
	return application.applicationPath(path)
}

// ApplyApplicationContext 在全局中间件执行后，把请求切换为当前应用的
// Root 与 Pathinfo；应用中间件和路由随后看到的行为与 ThinkPHP 一致。
func (r *Request) ApplyApplicationContext() error {
	if r == nil {
		return fmt.Errorf("%w: 请求为空", ErrInvalidApplicationPath)
	}
	application, exists := r.ApplicationContext()
	if !exists {
		return nil
	}
	rewritten := application.RewrittenPath()
	if rewritten == "" {
		rewritten = "/"
	}
	parsed, err := url.ParseRequestURI(rewritten)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasPrefix(parsed.Path, "/") {
		return fmt.Errorf("%w: 重写路径 %q 非法", ErrInvalidApplicationPath, rewritten)
	}
	r.metadataMu.Lock()
	if r.raw == nil || r.raw.URL == nil {
		r.metadataMu.Unlock()
		return fmt.Errorf("%w: 原始 URL 为空", ErrInvalidApplicationPath)
	}
	cloned := r.raw.Clone(r.raw.Context())
	cloned.Body = r.raw.Body
	cloned.GetBody = r.raw.GetBody
	cloned.URL.Path = parsed.Path
	cloned.URL.RawPath = ""
	cloned.RequestURI = cloned.URL.RequestURI()
	r.raw = cloned
	r.rootValue = strings.TrimSuffix(application.PathPrefix(), "/")
	r.rootSet = true
	r.pathinfoValue = strings.TrimLeft(parsed.Path, "/")
	r.pathinfoSet = true
	r.metadataMu.Unlock()
	return nil
}
