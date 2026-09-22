package http

import (
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
)

func nativeResolverApplications(names ...string) map[string]*Http {
	applications := make(map[string]*Http, len(names))
	for _, name := range names {
		applications[name] = &Http{}
	}
	return applications
}

// TestApplicationResolverMatchesThinkPHPMultiAppRules 验证默认应用、路径映射、
// 禁止列表、应用快速访问和显式入口遵循同一组稳定解析规则。
func TestApplicationResolverMatchesThinkPHPMultiAppRules(t *testing.T) {
	applications := nativeResolverApplications("index", "admin", "blocked")
	configuration := map[string]interface{}{
		"default_app":   "index",
		"app_map":       map[string]interface{}{"backend": "admin"},
		"domain_bind":   map[string]interface{}{},
		"deny_app_list": []interface{}{"blocked"},
		"app_express":   false,
	}
	resolver, err := newApplicationResolver(applications, configuration, "")
	if err != nil {
		t.Fatalf("创建多应用解析器失败: %v", err)
	}
	tests := []struct {
		name      string
		host      string
		path      string
		wantName  string
		wantPath  string
		wantRoot  string
		wantURL   string
		wantError error
	}{
		{name: "default", host: "example.com", path: "/", wantName: "index", wantPath: "/", wantURL: "/index"},
		{name: "named", host: "example.com", path: "/index/users", wantName: "index", wantPath: "/users", wantRoot: "/index", wantURL: "/index"},
		{name: "named with suffix", host: "example.com", path: "/index.html/users", wantName: "index", wantPath: "/users", wantRoot: "/index", wantURL: "/index"},
		{name: "mapped", host: "example.com", path: "/backend/users", wantName: "admin", wantPath: "/users", wantRoot: "/backend", wantURL: "/backend"},
		{name: "mapped with suffix", host: "example.com", path: "/backend.html/users", wantName: "admin", wantPath: "/users", wantRoot: "/backend", wantURL: "/backend"},
		{name: "mapped target hidden", host: "example.com", path: "/admin/users", wantError: errApplicationNotFound},
		{name: "denied", host: "example.com", path: "/blocked/users", wantError: errApplicationDenied},
		{name: "unknown", host: "example.com", path: "/missing/users", wantError: errApplicationNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(stdhttp.MethodGet, "http://"+test.host+test.path, nil)
			resolution, err := resolver.resolve(request)
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("解析错误不正确: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析请求失败: %v", err)
			}
			if resolution.name != test.wantName || resolution.rewrittenPath != test.wantPath || resolution.pathPrefix != test.wantRoot || resolution.urlPrefix != test.wantURL {
				t.Fatalf("解析结果错误: %#v", resolution)
			}
		})
	}

	express, err := newApplicationResolver(nativeResolverApplications("index"), map[string]interface{}{"default_app": "index", "app_express": true}, "")
	if err != nil {
		t.Fatalf("创建快速访问解析器失败: %v", err)
	}
	resolution, err := express.resolve(httptest.NewRequest(stdhttp.MethodGet, "http://example.com/users", nil))
	if err != nil || resolution.name != "index" || resolution.rewrittenPath != "/users" || resolution.urlPrefix != "" {
		t.Fatalf("快速访问默认应用错误: resolution=%#v err=%v", resolution, err)
	}

	bound, err := newApplicationResolver(applications, configuration, "admin")
	if err != nil {
		t.Fatalf("创建显式入口解析器失败: %v", err)
	}
	resolution, err = bound.resolve(httptest.NewRequest(stdhttp.MethodGet, "http://example.com/users", nil))
	if err != nil || resolution.name != "admin" || !resolution.domainBound || resolution.urlPrefix != "" {
		t.Fatalf("显式入口解析错误: resolution=%#v err=%v", resolution, err)
	}
}

// TestApplicationResolverDomainBindings 验证完整域名、子域名、后缀通配和
// 全域通配均保持 ThinkPHP 的绑定优先级，绑定请求不再生成应用路径前缀。
func TestApplicationResolverDomainBindings(t *testing.T) {
	resolver, err := newApplicationResolver(
		nativeResolverApplications("index", "admin"),
		map[string]interface{}{
			"default_app": "index",
			"domain_bind": map[string]interface{}{
				"admin.example.com": "admin",
				"api":               "admin",
				"*.tenant.test":     "admin",
				"*":                 "index",
			},
		},
		"",
	)
	if err != nil {
		t.Fatalf("创建域名解析器失败: %v", err)
	}
	tests := []struct {
		host string
		want string
	}{
		{host: "admin.example.com", want: "admin"},
		{host: "api.example.net", want: "admin"},
		{host: "shop.tenant.test", want: "admin"},
		{host: "other.example.net", want: "index"},
	}
	for _, test := range tests {
		request := httptest.NewRequest(stdhttp.MethodGet, "http://"+test.host+"/users", nil)
		request.Host = test.host
		resolution, err := resolver.resolve(request)
		if err != nil || resolution.name != test.want || !resolution.domainBound || resolution.urlPrefix != "" {
			t.Errorf("域名 %q 解析错误: resolution=%#v err=%v", test.host, resolution, err)
		}
	}
}

// TestApplicationResolverRejectsInvalidConfiguration 验证启动阶段拒绝所有
// 会造成应用越权、歧义映射或类型漂移的多应用配置。
func TestApplicationResolverRejectsInvalidConfiguration(t *testing.T) {
	applications := nativeResolverApplications("index", "admin")
	tests := []struct {
		name          string
		applications  map[string]*Http
		configuration map[string]interface{}
		explicit      string
		target        error
	}{
		{name: "empty applications", target: framework.ErrNoApplications},
		{name: "missing default", applications: applications, configuration: map[string]interface{}{"default_app": "missing"}, target: errInvalidApplication},
		{name: "invalid default type", applications: applications, configuration: map[string]interface{}{"default_app": 7}, target: errInvalidApplication},
		{name: "missing explicit", applications: applications, configuration: map[string]interface{}{"default_app": "index"}, explicit: "missing", target: errInvalidApplication},
		{name: "denied default", applications: applications, configuration: map[string]interface{}{"default_app": "index", "deny_app_list": []interface{}{"index"}}, target: errInvalidApplication},
		{name: "invalid deny type", applications: applications, configuration: map[string]interface{}{"default_app": "index", "deny_app_list": "admin"}, target: errInvalidApplication},
		{name: "unknown denied app", applications: applications, configuration: map[string]interface{}{"default_app": "index", "deny_app_list": []interface{}{"missing"}}, target: errInvalidApplication},
		{name: "invalid map type", applications: applications, configuration: map[string]interface{}{"default_app": "index", "app_map": []interface{}{}}, target: errInvalidApplication},
		{name: "unknown map target", applications: applications, configuration: map[string]interface{}{"default_app": "index", "app_map": map[string]interface{}{"api": "missing"}}, target: errInvalidApplication},
		{name: "invalid domain wildcard", applications: applications, configuration: map[string]interface{}{"default_app": "index", "domain_bind": map[string]interface{}{"api.*.test": "admin"}}, target: errInvalidApplication},
		{name: "normalized wildcard collision", applications: applications, configuration: map[string]interface{}{"default_app": "index", "domain_bind": map[string]interface{}{"*.Example.test": "admin", "*.example.test": "index"}}, target: errInvalidApplication},
		{name: "invalid express", applications: applications, configuration: map[string]interface{}{"default_app": "index", "app_express": "yes"}, target: errInvalidApplication},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := newApplicationResolver(test.applications, test.configuration, test.explicit)
			if !errors.Is(err, test.target) {
				t.Fatalf("配置错误不正确: got=%v want=%v", err, test.target)
			}
		})
	}
}

// TestApplicationResolverRejectsMalformedRequests 验证 Host 和编码路径在进入
// 应用选择前完成严格校验，避免不同解析层对同一 URL 得出不同应用。
func TestApplicationResolverRejectsMalformedRequests(t *testing.T) {
	resolver, err := newApplicationResolver(nativeResolverApplications("index"), map[string]interface{}{"default_app": "index"}, "")
	if err != nil {
		t.Fatalf("创建请求校验解析器失败: %v", err)
	}
	requests := []*stdhttp.Request{
		nil,
		{Host: "example.com", URL: nil},
		httptest.NewRequest(stdhttp.MethodGet, "http://example.com/%2e%2e/admin", nil),
		httptest.NewRequest(stdhttp.MethodGet, "http://example.com/admin", nil),
	}
	requests[3].Host = "bad/host"
	for index, request := range requests {
		if _, err := resolver.resolve(request); !errors.Is(err, errInvalidAppRequest) {
			t.Errorf("第 %d 个畸形请求应失败: %v", index+1, err)
		}
	}
	if got, err := normalizeApplicationHost("[2001:db8::1]:8080"); err != nil || got != "2001:db8::1" {
		t.Fatalf("IPv6 Host 规范化错误: host=%q err=%v", got, err)
	}
	if got, err := applicationStringList([]interface{}{"index", "admin"}, "apps"); err != nil || !reflect.DeepEqual(got, []string{"index", "admin"}) {
		t.Fatalf("字符串列表兼容输入错误: list=%#v err=%v", got, err)
	}
}
