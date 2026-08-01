package framework

import (
	"errors"
	"net/http"
	"net/url"
	"testing"

	"thinkgo/framework/config"
)

func newApplicationResolverTestManager(settings map[string]interface{}) *ApplicationManager {
	configuration := config.NewConfig()
	for key, value := range settings {
		configuration.Set("app."+key, value)
	}
	applications := map[string]*App{}
	for _, name := range []string{"index", "admin", "api", "blocked"} {
		applications[name] = &App{ApplicationName: name, config: configuration}
	}
	return &ApplicationManager{
		applicationNames: []string{"index", "admin", "api", "blocked"},
		applications:     applications,
		defaultAppName:   "index",
	}
}

func newApplicationResolverRequest(host, path string) *http.Request {
	return &http.Request{
		Host: host,
		URL:  &url.URL{Path: path},
	}
}

func TestApplicationResolverPrefersExactSubdomainAndWildcardBindings(t *testing.T) {
	manager := newApplicationResolverTestManager(map[string]interface{}{
		"domain_bind": map[string]interface{}{
			"admin.example.com":     "admin",
			"admin":                 "api",
			"*.service.example.com": "api",
			"*":                     "index",
		},
	})
	resolver, err := NewApplicationResolver(manager)
	if err != nil {
		t.Fatalf("创建应用解析器失败: %v", err)
	}

	tests := []struct {
		name          string
		host          string
		wantName      string
		wantPath      string
		wantPrefix    string
		wantDomainApp bool
	}{
		{name: "精确域名优先", host: "Admin.Example.com:8080", wantName: "admin", wantPath: "/users", wantDomainApp: true},
		{name: "子域名绑定", host: "admin.example.test", wantName: "api", wantPath: "/users", wantDomainApp: true},
		{name: "通配域名绑定", host: "v1.service.example.com", wantName: "api", wantPath: "/users", wantDomainApp: true},
		{name: "全局通配域名", host: "other.example.com", wantName: "index", wantPath: "/users", wantDomainApp: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolution, resolveErr := resolver.Resolve(newApplicationResolverRequest(test.host, "/users"))
			if resolveErr != nil {
				t.Fatalf("应用解析失败: %v", resolveErr)
			}
			if resolution.Name != test.wantName || resolution.RewrittenPath != test.wantPath || resolution.PathPrefix != test.wantPrefix || resolution.DomainBound != test.wantDomainApp {
				t.Fatalf("解析结果错误: %#v", resolution)
			}
		})
	}
}

func TestApplicationResolverMapsAndRewritesPathApplications(t *testing.T) {
	manager := newApplicationResolverTestManager(map[string]interface{}{
		"default_app": "index",
		"app_map": map[string]interface{}{
			"web": "index",
		},
		"deny_app_list": []interface{}{"blocked"},
		"app_express":   false,
	})
	resolver, err := NewApplicationResolver(manager)
	if err != nil {
		t.Fatalf("创建应用解析器失败: %v", err)
	}

	tests := []struct {
		name       string
		path       string
		wantName   string
		wantPath   string
		wantPrefix string
		wantErr    error
	}{
		{name: "应用路径带子路径", path: "/admin/users", wantName: "admin", wantPath: "/users", wantPrefix: "/admin"},
		{name: "应用路径无子路径", path: "/admin", wantName: "admin", wantPath: "/", wantPrefix: "/admin"},
		{name: "映射别名", path: "/web/users", wantName: "index", wantPath: "/users", wantPrefix: "/web"},
		{name: "默认应用", path: "/", wantName: "index", wantPath: "/", wantPrefix: ""},
		{name: "未知应用", path: "/missing/users", wantErr: ErrApplicationNotFound},
		{name: "拒绝应用", path: "/blocked/users", wantErr: ErrApplicationDenied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := newApplicationResolverRequest("localhost", test.path)
			request.URL.RawQuery = "trace=1"
			resolution, resolveErr := resolver.Resolve(request)
			if test.wantErr != nil {
				if !errors.Is(resolveErr, test.wantErr) {
					t.Fatalf("错误类型不匹配: got %v, want %v", resolveErr, test.wantErr)
				}
				return
			}
			if resolveErr != nil {
				t.Fatalf("应用解析失败: %v", resolveErr)
			}
			if resolution.Name != test.wantName || resolution.RewrittenPath != test.wantPath || resolution.PathPrefix != test.wantPrefix {
				t.Fatalf("解析结果错误: %#v", resolution)
			}
			if request.URL.RawQuery != "trace=1" {
				t.Fatal("解析器不得修改原请求查询参数")
			}
		})
	}
}

func TestApplicationResolverExpressFallsBackWithoutStrippingUnknownPath(t *testing.T) {
	manager := newApplicationResolverTestManager(map[string]interface{}{
		"default_app": "index",
		"app_express": true,
	})
	resolver, err := NewApplicationResolver(manager)
	if err != nil {
		t.Fatalf("创建应用解析器失败: %v", err)
	}
	resolution, err := resolver.Resolve(newApplicationResolverRequest("localhost", "/missing/users"))
	if err != nil {
		t.Fatalf("app_express=true 时未知路径应回退默认应用: %v", err)
	}
	if resolution.Name != "index" || resolution.RewrittenPath != "/missing/users" || resolution.PathPrefix != "" {
		t.Fatalf("回退解析结果错误: %#v", resolution)
	}
}

func TestApplicationResolverUsesSingleApplicationAsDefaultPathScope(t *testing.T) {
	configuration := config.NewConfig()
	manager := &ApplicationManager{
		applicationNames: []string{"index"},
		applications: map[string]*App{
			"index": {ApplicationName: "index", config: configuration},
		},
		defaultAppName: "index",
	}
	resolver, err := NewApplicationResolver(manager)
	if err != nil {
		t.Fatalf("创建单应用解析器失败: %v", err)
	}
	resolution, err := resolver.Resolve(newApplicationResolverRequest("localhost", "/api/users"))
	if err != nil {
		t.Fatalf("单应用未知首段应回退到默认应用: %v", err)
	}
	if resolution.Name != "index" || resolution.RewrittenPath != "/api/users" || resolution.PathPrefix != "" {
		t.Fatalf("单应用默认路径范围错误: %#v", resolution)
	}
}

func TestApplicationResolverRejectsUnsafeEncodedPaths(t *testing.T) {
	manager := newApplicationResolverTestManager(nil)
	resolver, err := NewApplicationResolver(manager)
	if err != nil {
		t.Fatalf("创建应用解析器失败: %v", err)
	}
	tests := []*url.URL{
		{Path: "/admin/users", RawPath: "/%2Fadmin/users"},
		{Path: "/admin/users", RawPath: "/admin%5Cusers"},
		{Path: "/admin/users", RawPath: "/%2e%2e/admin"},
		{Path: "/admin/users", RawPath: "/admin/%00"},
	}
	for _, requestURL := range tests {
		_, resolveErr := resolver.Resolve(&http.Request{Host: "localhost", URL: requestURL})
		if !errors.Is(resolveErr, ErrInvalidApplicationRequest) {
			t.Fatalf("非法编码路径应返回 ErrInvalidApplicationRequest: url=%#v err=%v", requestURL, resolveErr)
		}
	}
}

func TestApplicationResolverRejectsInvalidConfigurationBeforeRequests(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]interface{}
	}{
		{name: "默认应用不存在", settings: map[string]interface{}{"default_app": "missing"}},
		{name: "应用映射目标不存在", settings: map[string]interface{}{"app_map": map[string]interface{}{"web": "missing"}}},
		{name: "域名绑定目标不存在", settings: map[string]interface{}{"domain_bind": map[string]interface{}{"admin.example.com": "missing"}}},
		{name: "拒绝列表目标不存在", settings: map[string]interface{}{"deny_app_list": []interface{}{"missing"}}},
		{name: "域名包含路径分隔符", settings: map[string]interface{}{"domain_bind": map[string]interface{}{"admin/example.com": "admin"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewApplicationResolver(newApplicationResolverTestManager(test.settings)); !errors.Is(err, ErrInvalidApplicationResolverConfig) {
				t.Fatalf("非法解析配置应返回 ErrInvalidApplicationResolverConfig: %v", err)
			}
		})
	}
}
