package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

type generatedSkeletonController struct{}

// Index 保持标准生成骨架的首页行为。
func (*generatedSkeletonController) Index() string {
	return "ThinkGo"
}

// Hello 保持标准生成骨架的可变参数动作签名。
func (*generatedSkeletonController) Hello(names ...string) string {
	name := "ThinkPHP"
	if len(names) > 0 && names[0] != "" {
		name = names[0]
	}
	return "hello," + name
}

// TestGeneratedSkeletonNativeDispatch 验证生成项目使用真实组件清单、请求工厂、
// 默认路由配置和两个应用时，动态路由与项目 public 目录仍按各自路径分发。
func TestGeneratedSkeletonNativeDispatch(t *testing.T) {
	for _, directRun := range []bool{false, true} {
		kernel, _ := newGeneratedSkeletonHTTP(t, ``, false)
		for _, test := range []struct {
			method string
			path   string
			status int
			body   string
		}{
			{http.MethodGet, "/", http.StatusOK, "ThinkGo"},
			{http.MethodGet, "/index", http.StatusOK, "ThinkGo"},
			{http.MethodGet, "/index/hello/CLI20260922", http.StatusOK, "hello,CLI20260922"},
			{http.MethodGet, "/admin/hello/Admin", http.StatusOK, "hello,Admin"},
			{http.MethodGet, "/index/hello/CLI20260922.html?name=Query", http.StatusOK, "hello,CLI20260922"},
			{http.MethodGet, "/index/not_found", http.StatusNotFound, ""},
			{http.MethodGet, "/robots.txt", http.StatusOK, "User-agent: *\nDisallow:\n"},
			{http.MethodGet, "/assets/site.css", http.StatusOK, "body { color: black; }"},
			{http.MethodHead, "/robots.txt", http.StatusOK, ""},
			{http.MethodGet, "/index/robots.txt", http.StatusNotFound, ""},
			{http.MethodGet, "/missing/route", http.StatusNotFound, ""},
			{http.MethodGet, "/assets/fallback", http.StatusNotFound, ""},
			{http.MethodPost, "/robots.txt", http.StatusNotFound, ""},
		} {
			recorder := serveGeneratedSkeletonRequest(t, kernel, test.method, test.path, directRun, nil)
			if recorder.Code != test.status || test.body != "" && recorder.Body.String() != test.body {
				t.Errorf("Run=%t %s %s: status=%d body=%q，期望 status=%d body=%q", directRun, test.method, test.path, recorder.Code, recorder.Body.String(), test.status, test.body)
			}
			if test.method == http.MethodHead && recorder.Body.Len() != 0 {
				t.Errorf("HEAD 不得发送文件正文: %q", recorder.Body.String())
			}
			if recorder.Header().Get("Location") != "" {
				t.Errorf("分发不得依赖跳转: %q", recorder.Header().Get("Location"))
			}
		}
	}
}

// TestNativePublicFilesKeepSecurityBoundaries 验证公共文件仍经过全局认证，
// 应用映射隐藏、禁止列表、路径校验和文件根边界不会被静态适配绕过。
func TestNativePublicFilesKeepSecurityBoundaries(t *testing.T) {
	for _, directRun := range []bool{false, true} {
		kernel, basePath := newGeneratedSkeletonHTTP(t, `,"app_map":{"backend":"admin","restricted":"blocked"},"deny_app_list":["blocked"]`, true)
		for _, path := range []string{"/robots.txt", "/assets/site.css"} {
			recorder := serveGeneratedSkeletonRequest(t, kernel, http.MethodGet, path, directRun, nil)
			if recorder.Code != http.StatusUnauthorized || recorder.Body.String() != "global denied" {
				t.Errorf("Run=%t 静态文件绕过全局认证 %s: status=%d body=%q", directRun, path, recorder.Code, recorder.Body.String())
			}
		}
		for _, test := range []struct {
			path   string
			status int
		}{
			{"/robots.txt", http.StatusOK},
			{"/admin/secret.txt", http.StatusNotFound},
			{"/ADMIN/secret.txt", http.StatusNotFound},
			{"/AdMiN/secret.txt", http.StatusNotFound},
			{"/admin%20/secret.txt", http.StatusNotFound},
			{"/blocked/secret.txt", http.StatusNotFound},
			{"/BLOCKED/secret.txt", http.StatusNotFound},
			{"/blocked%20/secret.txt", http.StatusNotFound},
			{"/restricted/secret.txt", http.StatusNotFound},
			{"/RESTRICTED/secret.txt", http.StatusNotFound},
			{"/assets/../secret.txt", http.StatusNotFound},
			{"/assets%2fsite.css", http.StatusBadRequest},
			{"/%2e%2e/secret.txt", http.StatusBadRequest},
			{"/backend/hello/Admin", http.StatusForbidden},
		} {
			recorder := serveGeneratedSkeletonRequest(t, kernel, http.MethodGet, test.path, directRun, http.Header{"Authorization": []string{"allowed"}})
			if recorder.Code != test.status {
				t.Errorf("Run=%t 访问边界错误 %s: status=%d body=%q", directRun, test.path, recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "private-secret") {
				t.Errorf("Run=%t 泄露私有文件 %s", directRun, test.path)
			}
		}
		linkedPath := filepath.Join(basePath, "public", "linked.txt")
		if err := os.Symlink(filepath.Join(basePath, "secret.txt"), linkedPath); err == nil {
			recorder := serveGeneratedSkeletonRequest(t, kernel, http.MethodGet, "/linked.txt", directRun, http.Header{"Authorization": []string{"allowed"}})
			if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "private-secret") {
				t.Errorf("符号链接逃逸保护失效: status=%d body=%q", recorder.Code, recorder.Body.String())
			}
		} else {
			t.Logf("当前平台不能创建符号链接，本项由支持符号链接的平台验证: %v", err)
		}
	}
}

// TestNativePublicFilesPreserveHTTPResponses 验证静态分发保留分段、缓存协商、
// HEAD 和 Host 白名单语义，且同名应用目录不会改变项目公共文件的原始 URL。
func TestNativePublicFilesPreserveHTTPResponses(t *testing.T) {
	kernel, basePath := newGeneratedSkeletonHTTP(t, ``, false)
	publicDirectory := filepath.Join(basePath, "public", "index")
	if err := os.MkdirAll(publicDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicDirectory, "asset.txt"), []byte("application-prefixed asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	initial := serveGeneratedSkeletonRequest(t, kernel, http.MethodGet, "/robots.txt", false, nil)
	modified := initial.Header().Get("Last-Modified")
	if initial.Code != http.StatusOK || modified == "" {
		t.Fatalf("静态响应缺少缓存协商依据: status=%d Last-Modified=%q", initial.Code, modified)
	}
	for _, test := range []struct {
		method  string
		path    string
		headers http.Header
		status  int
		body    string
	}{
		{http.MethodGet, "/robots.txt", http.Header{"Range": []string{"bytes=0-3"}}, http.StatusPartialContent, "User"},
		{http.MethodHead, "/robots.txt", nil, http.StatusOK, ""},
		{http.MethodGet, "/robots.txt", http.Header{"If-Modified-Since": []string{modified}}, http.StatusNotModified, ""},
		{http.MethodGet, "/index/asset.txt", nil, http.StatusOK, "application-prefixed asset"},
		{http.MethodHead, "/missing/file.txt", nil, http.StatusNotFound, ""},
	} {
		recorder := serveGeneratedSkeletonRequest(t, kernel, test.method, test.path, false, test.headers)
		if recorder.Code != test.status || recorder.Body.String() != test.body {
			t.Errorf("%s %s 协议响应错误: status=%d body=%q", test.method, test.path, recorder.Code, recorder.Body.String())
		}
	}
	raw := httptest.NewRequest(http.MethodGet, "http://evil.example.com/robots.txt", nil)
	recorder := httptest.NewRecorder()
	kernel.ServeHTTP(recorder, raw)
	if recorder.Code != http.StatusMisdirectedRequest || strings.Contains(recorder.Body.String(), "User-agent") {
		t.Fatalf("静态适配绕过 Host 白名单: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

// newGeneratedSkeletonHTTP 复用生成器的组件装配方式，避免纯回调测试遗漏控制器路由问题。
func newGeneratedSkeletonHTTP(t *testing.T, extraConfig string, requireAuthorization bool) (*Http, string) {
	t.Helper()
	basePath := t.TempDir()
	writeNativeHTTPConfig(t, basePath, `{"app_env":"test","default_app":"index","with_route":true,"public_path":"public","server":{"host":"127.0.0.1","port":8080,"allowed_hosts":["example.com"]},"compression":{"enable":false}`+extraConfig+`}`)
	files := map[string]string{
		"config/route.json":            `{"url_route_must":false,"default_route_pattern":"[\\w\\.]+","default_controller":"Index","default_action":"index","url_html_suffix":"html","controller_layer":"controller"}`,
		"public/robots.txt":            "User-agent: *\nDisallow:\n",
		"public/assets/site.css":       "body { color: black; }",
		"public/admin/secret.txt":      "private-secret",
		"public/blocked/secret.txt":    "private-secret",
		"public/restricted/secret.txt": "private-secret",
		"secret.txt":                   "private-secret",
	}
	for name, content := range files {
		path := filepath.Join(basePath, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	globalLoader := func(app *framework.App) error {
		if err := app.BindFactory(string(framework.ServiceRequest), func(raw *http.Request, options ...fwcontext.RequestOption) (*fwcontext.Request, error) {
			return fwcontext.NewRequest(raw, options...)
		}); err != nil {
			return err
		}
		return app.RegisterGlobalMiddleware(func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			if requireAuthorization && request.Raw().Header.Get("Authorization") != "allowed" {
				return fwcontext.NewResponse().Code(http.StatusUnauthorized).Content("global denied")
			}
			return next(request)
		})
	}
	loader := func(app *framework.App) error {
		if err := app.RegisterApplicationMiddleware(func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			if requireAuthorization {
				return fwcontext.NewResponse().Code(http.StatusForbidden).Content("application denied")
			}
			return next(request)
		}); err != nil {
			return err
		}
		return app.RegisterApplicationComponents(framework.ApplicationComponents{
			Controllers: map[string]interface{}{"Index": &generatedSkeletonController{}},
			RouteLoader: func(current *framework.App) error {
				current.Route().Get("/", "Index@Index")
				current.Route().Get("hello/:name", "Index@Hello")
				current.Route().Get("assets/fallback", func() string { return "unexpected application fallback" })
				return nil
			},
		})
	}
	app := framework.NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	definitions := []framework.ApplicationDefinition{
		{Name: "index", Register: loader},
		{Name: "admin", Register: loader},
	}
	if requireAuthorization {
		definitions = append(definitions, framework.ApplicationDefinition{Name: "blocked", Register: loader})
	}
	if err := app.RegisterApplications(globalLoader, definitions...); err != nil {
		t.Fatal(err)
	}
	kernel, err := NewHttp(app)
	if err != nil {
		t.Fatal(err)
	}
	return kernel, basePath
}

// serveGeneratedSkeletonRequest 同时验证网络适配入口和不监听端口的直接 Run 生命周期。
func serveGeneratedSkeletonRequest(t *testing.T, kernel *Http, method, path string, directRun bool, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	raw := httptest.NewRequest(method, "http://example.com"+path, nil)
	for key, values := range headers {
		raw.Header[key] = values
	}
	if !directRun {
		kernel.ServeHTTP(recorder, raw)
		return recorder
	}
	request := fwcontext.MustNewRequest(raw, fwcontext.WithResponseWriter(recorder))
	response := kernel.Run(request)
	defer kernel.End(response)
	if !response.Committed() {
		if err := response.Send(recorder); err != nil {
			t.Fatal(err)
		}
	}
	return recorder
}
