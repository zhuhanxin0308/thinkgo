package context

import (
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	frameworkcookie "github.com/zhuhanxin0308/thinkgo/framework/cookie"
	frameworkenv "github.com/zhuhanxin0308/thinkgo/framework/env"
	frameworksession "github.com/zhuhanxin0308/thinkgo/framework/session"
	sessiondriver "github.com/zhuhanxin0308/thinkgo/framework/session/driver"
)

// TestRequestThinkPHPDomainAndRuntimeMetadataAPI 验证根域名、子域名、请求时间、
// 脚本根路径和资源协商遵循本地 ThinkPHP Request.php 的默认规则。
func TestRequestThinkPHPDomainAndRuntimeMetadataAPI(t *testing.T) {
	raw := httptest.NewRequest(http.MethodGet, "https://api.shop.example.co.uk:8443/index.php/admin?from=test", nil)
	raw.Header.Set("Accept", "application/json, text/html;q=0.8")
	request := newRequestForTest(t, raw).
		WithServer(map[string]interface{}{
			"SCRIPT_FILENAME":    `C:\site\public\index.php`,
			"SCRIPT_NAME":        "/index.php",
			"REQUEST_TIME":       "1700000000",
			"REQUEST_TIME_FLOAT": "1700000000.25",
		})

	if request.RootDomain() != "example.co.uk" || request.SubDomain() != "api.shop" {
		t.Fatalf("根域名或子域名计算错误: root=%q sub=%q", request.RootDomain(), request.SubDomain())
	}
	if request.SetRootDomain("shop.example.co.uk").SetSubDomain("api").SetPanDomain("tenant") != request {
		t.Fatal("域名设置 API 必须支持链式调用")
	}
	if request.RootDomain() != "shop.example.co.uk" || request.SubDomain() != "api" || request.PanDomain() != "tenant" {
		t.Fatalf("显式域名设置未生效: root=%q sub=%q pan=%q", request.RootDomain(), request.SubDomain(), request.PanDomain())
	}
	if request.BaseFile() != "/index.php" || request.BaseFile(true) != "https://api.shop.example.co.uk:8443/index.php" {
		t.Fatalf("BaseFile 结果错误: relative=%q complete=%q", request.BaseFile(), request.BaseFile(true))
	}
	if request.Root() != "/index.php" || request.RootUrl() != "" {
		t.Fatalf("Root 或 RootUrl 结果错误: root=%q root_url=%q", request.Root(), request.RootUrl())
	}
	if got, ok := request.Time().(int64); !ok || got != 1700000000 {
		t.Fatalf("整数请求时间错误: %#v", request.Time())
	}
	if got, ok := request.Time(true).(float64); !ok || got != 1700000000.25 {
		t.Fatalf("浮点请求时间错误: %#v", request.Time(true))
	}
	if request.Type() != "json" {
		t.Fatalf("默认 Accept 资源类型识别错误: %q", request.Type())
	}
	request.MimeType("problem", "application/problem+json")
	raw.Header.Set("Accept", "application/problem+json")
	if request.Type() != "problem" {
		t.Fatalf("自定义资源类型未生效: %q", request.Type())
	}
}

// TestRequestThinkPHPInitializationAndMiddlewareAPI 验证 with* 初始化入口会替换
// 对应输入集合，而 setRoute 和 withMiddleware 保持 ThinkPHP 的合并语义。
func TestRequestThinkPHPInitializationAndMiddlewareAPI(t *testing.T) {
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/original?source=raw", strings.NewReader(`{"body":"raw"}`))
	raw.Header.Set("Content-Type", "application/json")
	request := newRequestForTest(t, raw).
		WithGet(map[string]interface{}{"source": "get", "name": " thinkgo "}).
		WithPost(map[string]interface{}{"body": "post"}).
		WithCookie(map[string]interface{}{"theme": "dark"}).
		WithHeader(map[string]string{"X-Framework": "ThinkGo", "Content-Type": "application/json"}).
		WithServer(map[string]interface{}{"CUSTOM_KEY": "server"}).
		WithRoute(map[string]interface{}{"id": 7}).
		WithMiddleware(map[string]interface{}{"tenant": "alpha"})
	request.SetRoute(map[string]interface{}{"slug": "article"})
	request.SetCookie("language", "zh-CN")
	request.Filter(strings.TrimSpace, strings.ToUpper)

	if request.Get("source") != "GET" || request.Get("name") != "THINKGO" || request.Post("body") != "POST" {
		t.Fatalf("withGet/withPost 或过滤器未生效: get=%q name=%q post=%q", request.Get("source"), request.Get("name"), request.Post("body"))
	}
	if request.Cookie("theme") != "DARK" || request.Cookie("language") != "ZH-CN" || request.Cookie("missing", "fallback") != "FALLBACK" {
		t.Fatalf("Cookie 初始化或默认值错误: theme=%q language=%q", request.Cookie("theme"), request.Cookie("language"))
	}
	if request.Header("x-framework") != "THINKGO" || request.Server("custom_key") != "SERVER" {
		t.Fatalf("Header 或 Server 初始化错误: header=%q server=%q", request.Header("x-framework"), request.Server("custom_key"))
	}
	if request.Route("id") != "7" || request.Route("slug") != "ARTICLE" {
		t.Fatalf("路由替换或合并错误: id=%q slug=%q", request.Route("id"), request.Route("slug"))
	}
	if request.Middleware("tenant") != "alpha" || request.Middleware("missing", "fallback") != "fallback" {
		t.Fatalf("中间件数据读取错误: tenant=%#v missing=%#v", request.Middleware("tenant"), request.Middleware("missing", "fallback"))
	}
	all, ok := request.Middleware().(map[string]interface{})
	if !ok || all["tenant"] != "alpha" {
		t.Fatalf("中间件数据快照错误: %#v", request.Middleware())
	}
	if request.FilterValue(" value ") != "VALUE" {
		t.Fatalf("FilterValue 未应用全局过滤规则: %q", request.FilterValue(" value "))
	}

	request.WithInput(`{"body":"input"}`)
	if request.GetInput() != `{"body":"input"}` || request.Put("body") != "INPUT" || request.Delete("body") != "INPUT" || request.Patch("body") != "INPUT" {
		t.Fatalf("withInput 未同步原始输入和 PUT 数据: input=%q put=%q", request.GetInput(), request.Put("body"))
	}

	file := &multipart.FileHeader{Filename: "avatar.png"}
	request.WithFiles(map[string]*multipart.FileHeader{"avatar": file})
	loaded, err := request.File("avatar")
	if err != nil || loaded == file || loaded.Filename != file.Filename {
		t.Fatalf("withFiles 应返回防御性文件元数据副本: file=%#v err=%v", loaded, err)
	}
}

// TestRequestThinkPHPParamMergeOrder 验证 Param 按 ThinkPHP 的 route、get、body
// 合并顺序处理同名键，并且中间件数据不会混入业务输入。
func TestRequestThinkPHPParamMergeOrder(t *testing.T) {
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/users?role=query", strings.NewReader(`{"role":"body"}`))
	raw.Header.Set("Content-Type", "application/json")
	request := newRequestForTest(t, raw).
		WithRoute(map[string]interface{}{"role": "route", "route_only": "yes"}).
		WithMiddleware(map[string]interface{}{"role": "middleware", "internal": "secret"})

	if request.Param("role") != "body" {
		t.Fatalf("body 应覆盖同名 get 和 route 参数，实际为 %q", request.Param("role"))
	}
	if request.Param("route_only") != "yes" || request.Param("internal") != "" {
		t.Fatalf("route 参数应参与 Param，中间件数据不得混入: route=%q internal=%q", request.Param("route_only"), request.Param("internal"))
	}
	all := request.All()
	if all["role"] != "body" {
		t.Fatalf("All 应采用同一合并顺序: %#v", all)
	}
	if _, exists := all["internal"]; exists {
		t.Fatalf("All 不得暴露中间件内部数据: %#v", all)
	}
}

// TestRequestThinkPHPEnvironmentSessionAndTokenAPI 验证 Env、Session 和一次性
// 表单令牌由请求对象直接提供，业务代码不需要接触内部透传键。
func TestRequestThinkPHPEnvironmentSessionAndTokenAPI(t *testing.T) {
	const environmentName = "THINKGO_REQUEST_COMPATIBILITY"
	t.Setenv(environmentName, "ready")
	environment := frameworkenv.NewEnv()

	raw := httptest.NewRequest(http.MethodPost, "http://example.com/form", nil)
	cookieFactory, err := frameworkcookie.NewCookie(map[string]interface{}{})
	if err != nil {
		t.Fatalf("创建 Cookie 工厂失败: %v", err)
	}
	manager, err := frameworksession.NewSession(map[string]interface{}{"name": "THINKGOSESSID"}, sessiondriver.NewMemory(), cookieFactory)
	if err != nil {
		t.Fatalf("创建 Session 管理器失败: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	requestSession, err := manager.NewRequestSession(raw, httptest.NewRecorder())
	if err != nil {
		t.Fatalf("创建请求 Session 失败: %v", err)
	}
	if err = requestSession.Set("uid", 1001); err != nil {
		t.Fatalf("设置 Session 数据失败: %v", err)
	}

	request := newRequestForTest(t, raw).WithEnv(environment).WithSession(requestSession)
	if request.Env(environmentName) != "ready" || request.Env("MISSING_ENV", "fallback") != "fallback" {
		t.Fatalf("Env 读取错误: value=%#v missing=%#v", request.Env(environmentName), request.Env("MISSING_ENV", "fallback"))
	}
	if request.Session("uid") != int64(1001) || request.Session("missing", "fallback") != "fallback" {
		t.Fatalf("Session 读取错误: uid=%#v missing=%#v", request.Session("uid"), request.Session("missing", "fallback"))
	}
	if values, ok := request.Session().(map[string]interface{}); !ok || values["uid"] != int64(1001) {
		t.Fatalf("Session 全量读取错误: %#v", request.Session())
	}

	token := request.BuildToken()
	if token == "" || !requestSession.Has("__token__") {
		t.Fatalf("BuildToken 未写入默认 Session 键: token=%q", token)
	}
	request.WithPost(map[string]interface{}{"__token__": token})
	if !request.CheckToken() || requestSession.Has("__token__") {
		t.Fatal("CheckToken 应验证并销毁一次性令牌")
	}
	if request.CheckToken() {
		t.Fatal("同一表单令牌不得重复使用")
	}
}

// TestRequestThinkPHPNetworkAndSecureKeyAPI 验证网络判断、移动端识别和请求级安全键。
func TestRequestThinkPHPNetworkAndSecureKeyAPI(t *testing.T) {
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	raw.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile")
	request := newRequestForTest(t, raw)

	if request.IsCli() || request.IsCgi() || !request.IsMobile() {
		t.Fatalf("运行模式或移动端识别错误: cli=%t cgi=%t mobile=%t", request.IsCli(), request.IsCgi(), request.IsMobile())
	}
	if !request.IsValidIP("192.0.2.1") || !request.IsValidIP("2001:db8::1", "ipv6") || request.IsValidIP("2001:db8::1", "ipv4") || request.IsValidIP("invalid") {
		t.Fatal("IP 类型校验结果错误")
	}
	if len(request.Ip2bin("192.0.2.1")) != 32 || len(request.Ip2bin("2001:db8::1")) != 128 || request.Ip2bin("invalid") != "" {
		t.Fatal("IP 二进制转换结果错误")
	}
	firstKey := request.SecureKey()
	if firstKey == "" || request.SecureKey() != firstKey {
		t.Fatal("SecureKey 必须在同一请求内稳定且非空")
	}
	second := newRequestForTest(t, httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	if second.SecureKey() == firstKey {
		t.Fatal("不同请求不得复用 SecureKey")
	}

	createdAt := request.Time(true).(float64)
	if createdAt <= 0 || time.Since(time.Unix(0, int64(createdAt*float64(time.Second)))) > time.Minute {
		t.Fatalf("默认请求时间不应偏离创建时刻: %v", createdAt)
	}
}
