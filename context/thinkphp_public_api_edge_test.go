package context

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	frameworkcookie "github.com/zhuhanxin0308/thinkgo/framework/cookie"
	frameworkenv "github.com/zhuhanxin0308/thinkgo/framework/env"
	frameworksession "github.com/zhuhanxin0308/thinkgo/framework/session"
	sessiondriver "github.com/zhuhanxin0308/thinkgo/framework/session/driver"
)

// TestRequestThinkPHPResponseWriterAndStrictJSON 验证 HTTP 内核可以绑定当前
// ResponseWriter，同时业务层直接 Json 绑定仍执行严格 JSON 校验。
func TestRequestThinkPHPResponseWriterAndStrictJSON(t *testing.T) {
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/users", strings.NewReader(`{"name":"alice"}`))
	raw.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	request, err := NewRequest(raw, WithResponseWriter(recorder))
	if err != nil {
		t.Fatalf("绑定 ResponseWriter 失败: %v", err)
	}
	writer, exists := request.ResponseWriter()
	if !exists || writer != recorder {
		t.Fatalf("ResponseWriter 绑定结果错误: writer=%#v exists=%t", writer, exists)
	}

	var payload struct {
		Name string `json:"name"`
	}
	if err = request.Json(&payload); err != nil || payload.Name != "alice" {
		t.Fatalf("严格 JSON 绑定失败: payload=%#v err=%v", payload, err)
	}

	var nilWriter *httptest.ResponseRecorder
	_, err = NewRequest(raw, WithResponseWriter(nilWriter))
	if !errors.Is(err, ErrInvalidResponseWriter) {
		t.Fatalf("类型化 nil writer 必须返回 ErrInvalidResponseWriter，实际为 %v", err)
	}
	if writer, exists = (*Request)(nil).ResponseWriter(); exists || writer != nil {
		t.Fatalf("空请求不应暴露 ResponseWriter: writer=%#v exists=%t", writer, exists)
	}
}

// TestRequestThinkPHPFormCookieRuleAndEnvironmentAPI 验证 withInput 的表单解析、
// setCookie 的原始 Cookie 合并，以及 Rule 和 Env 的请求级装配语义。
func TestRequestThinkPHPFormCookieRuleAndEnvironmentAPI(t *testing.T) {
	const environmentName = "THINKGO_REQUEST_OPTION_ENV"
	t.Setenv(environmentName, "enabled")
	environment := frameworkenv.NewEnv()

	raw := httptest.NewRequest(http.MethodPost, "http://example.com/form", nil)
	raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	raw.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
	request, err := NewRequest(raw, WithEnvService(environment))
	if err != nil {
		t.Fatalf("通过请求选项装配 Env 失败: %v", err)
	}
	request.WithInput("name=alice&tag=go&tag=framework")
	request.SetCookie("language", "zh-CN")
	rule := map[string]interface{}{"name": "user.read"}
	request.SetRule(rule)

	if request.Post("name") != "alice" || request.Post("tag") != "go,framework" {
		t.Fatalf("表单输入解析错误: name=%q tag=%q", request.Post("name"), request.Post("tag"))
	}
	if request.Cookie("theme") != "dark" || request.Cookie("language") != "zh-CN" {
		t.Fatalf("原始 Cookie 与增量 Cookie 合并错误: theme=%q language=%q", request.Cookie("theme"), request.Cookie("language"))
	}
	if !reflect.DeepEqual(request.Rule(), rule) {
		t.Fatalf("路由规则对象未按请求保存: %#v", request.Rule())
	}
	if request.Env(environmentName) != "enabled" {
		t.Fatalf("Env 请求选项未生效: %#v", request.Env(environmentName))
	}
	allEnvironment, ok := request.Env().(map[string]string)
	if !ok || allEnvironment[environmentName] != "enabled" {
		t.Fatalf("Env 全量快照错误: %#v", request.Env())
	}

	request.WithInput("bad=%zz")
	if request.Post("bad") != "" {
		t.Fatalf("非法表单不能替换已解析输入: %q", request.Post("bad"))
	}
	if got := newRequestForTest(t, raw).Env("missing", "fallback"); got != "fallback" {
		t.Fatalf("未装配 Env 时必须返回默认值: %#v", got)
	}
	if got := newRequestForTest(t, raw).Env(); !reflect.DeepEqual(got, map[string]string{}) {
		t.Fatalf("未装配 Env 时全量读取必须返回空集合: %#v", got)
	}
}

// TestRequestThinkPHPSessionAndTokenBranches 验证无 Session、幂等安全方法、
// 请求头令牌、自定义数据令牌以及非法令牌都遵循一次性校验规则。
func TestRequestThinkPHPSessionAndTokenBranches(t *testing.T) {
	withoutSession := newRequestForTest(t, httptest.NewRequest(http.MethodPost, "http://example.com/form", nil))
	if got := withoutSession.Session("missing", "fallback"); got != "fallback" {
		t.Fatalf("未装配 Session 时默认值错误: %#v", got)
	}
	if got := withoutSession.Session(); !reflect.DeepEqual(got, map[string]interface{}{}) {
		t.Fatalf("未装配 Session 时全量读取必须返回空集合: %#v", got)
	}
	if token := withoutSession.BuildToken(); token != "" || withoutSession.TokenError() == nil {
		t.Fatalf("无 Session 时不得生成令牌: token=%q err=%v", token, withoutSession.TokenError())
	}

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		request := newRequestForTest(t, httptest.NewRequest(method, "http://example.com/form", nil))
		if !request.CheckToken() {
			t.Fatalf("%s 请求不应要求表单令牌", method)
		}
	}

	requestSession, closeSession := newThinkPHPRequestSession(t)
	defer closeSession()
	request := newRequestForTest(t, httptest.NewRequest(http.MethodPost, "http://example.com/form", nil)).WithSession(requestSession)
	headerToken := request.BuildToken()
	request.WithHeader(map[string]string{"X-CSRF-TOKEN": headerToken})
	if !request.CheckToken() || requestSession.Has(defaultRequestTokenName) {
		t.Fatal("请求头令牌应校验成功并立即销毁")
	}

	request.WithHeader(map[string]string{})
	customToken := request.BuildToken("form_token")
	if !request.CheckToken("form_token", map[string]interface{}{"form_token": customToken}) {
		t.Fatal("显式数据映射中的自定义令牌应校验成功")
	}

	mismatch := request.BuildToken()
	if mismatch == "" || request.CheckToken(defaultRequestTokenName, map[string]interface{}{defaultRequestTokenName: "wrong"}) {
		t.Fatal("错误令牌不得通过校验")
	}
	if requestSession.Has(defaultRequestTokenName) {
		t.Fatal("错误令牌完成校验后也必须销毁，避免重复尝试")
	}

	if err := requestSession.Set("invalid_token", map[string]string{"value": "not-scalar"}); err != nil {
		t.Fatalf("准备非法令牌失败: %v", err)
	}
	if request.CheckToken("invalid_token", map[string]interface{}{"invalid_token": "value"}) || requestSession.Has("invalid_token") {
		t.Fatal("非标量 Session 令牌必须拒绝并销毁")
	}
}

// TestRequestThinkPHPMIMEBaseFileAndServerAPI 验证 MIME 映射可替换，并覆盖
// ThinkPHP 依赖的常用 SERVER 字段和入口文件推导路径。
func TestRequestThinkPHPMIMEBaseFileAndServerAPI(t *testing.T) {
	raw := httptest.NewRequest(http.MethodPost, "https://example.com:8443/index.php/users?id=7", strings.NewReader("body"))
	raw.RemoteAddr = "192.0.2.10:54321"
	raw.Header.Set("Content-Type", "text/plain")
	raw.Header.Set("X-Request-ID", "request-7")
	request := newRequestForTest(t, raw)

	expectedServer := map[string]string{
		"REQUEST_METHOD":      http.MethodPost,
		"REQUEST_URI":         "/index.php/users?id=7",
		"QUERY_STRING":        "id=7",
		"PATH_INFO":           "/index.php/users",
		"HTTP_HOST":           "example.com:8443",
		"SERVER_NAME":         "example.com",
		"SERVER_PORT":         "8443",
		"SERVER_PROTOCOL":     "HTTP/1.1",
		"REMOTE_ADDR":         "192.0.2.10",
		"REMOTE_PORT":         "54321",
		"REQUEST_SCHEME":      "https",
		"HTTPS":               "on",
		"CONTENT_TYPE":        "text/plain",
		"CONTENT_LENGTH":      "4",
		"HTTP_X_REQUEST_ID":   "request-7",
		"HTTP_CONTENT_TYPE":   "text/plain",
		"HTTP_CONTENT_LENGTH": "4",
	}
	for name, expected := range expectedServer {
		if actual := request.Server(name); actual != expected {
			t.Errorf("Server(%q)=%q，期望 %q", name, actual, expected)
		}
	}

	raw.Header.Set("Accept", "application/vnd.api+json")
	request.MimeType(map[string]string{"api": "application/vnd.api+json, application/problem+json"})
	if request.Type() != "api" {
		t.Fatalf("map[string]string MIME 定义未生效: %q", request.Type())
	}
	raw.Header.Set("Accept", "application/custom")
	request.MimeType(map[string]interface{}{"custom": "application/custom", "ignored": 7})
	if request.Type() != "custom" {
		t.Fatalf("map[string]interface{} MIME 定义未生效: %q", request.Type())
	}
	request.MimeType("custom", "application/replaced")
	raw.Header.Set("Accept", "application/replaced")
	if request.Type() != "custom" {
		t.Fatalf("同名 MIME 定义应被替换: %q", request.Type())
	}

	tests := []struct {
		name     string
		server   map[string]interface{}
		expected string
	}{
		{
			name: "PHP_SELF 回退",
			server: map[string]interface{}{
				"SCRIPT_FILENAME": "/srv/site/public/index.php",
				"SCRIPT_NAME":     "/shop/front.php/extra",
				"PHP_SELF":        "/shop/index.php/users",
			},
			expected: "/shop/index.php",
		},
		{
			name: "DOCUMENT_ROOT 回退",
			server: map[string]interface{}{
				"SCRIPT_FILENAME": "/srv/site/public/index.php",
				"DOCUMENT_ROOT":   "/srv/site/public",
			},
			expected: "/index.php",
		},
		{name: "无脚本入口", server: map[string]interface{}{}, expected: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := newRequestForTest(t, httptest.NewRequest(http.MethodGet, "http://example.com/", nil)).WithServer(test.server)
			if actual := current.BaseFile(); actual != test.expected {
				t.Fatalf("BaseFile()=%q，期望 %q", actual, test.expected)
			}
		})
	}
}

// TestResponseThinkPHPHeaderCookieDataAndCommitAPI 验证 Response 的集合头、
// Cookie option、基础数据类型以及已提交响应契约。
func TestResponseThinkPHPHeaderCookieDataAndCommitAPI(t *testing.T) {
	response := NewResponse().Header(map[string]string{"X-Framework": "ThinkGo"})
	response.Header(map[string]interface{}{"X-Version": "2", "X-Invalid": 7})
	response.Header(http.Header{"X-Trace": []string{"one", "two"}})
	if response.GetHeader("X-Framework") != "ThinkGo" || response.GetHeader("X-Version") != "2" {
		t.Fatalf("响应头映射设置错误: %#v", response.Headers())
	}
	if !reflect.DeepEqual(response.Headers().Values("X-Trace"), []string{"one", "two"}) {
		t.Fatalf("http.Header 多值未保留: %#v", response.Headers())
	}
	if !errors.Is(response.Error(), ErrInvalidResponseHeader) {
		t.Fatalf("非字符串响应头值必须返回 ErrInvalidResponseHeader: %v", response.Error())
	}

	validCookies := NewResponse().
		Cookie("seconds", "value", int64(60)).
		Cookie("text_seconds", "value", "120").
		Cookie("absolute", "value", time.Now().Add(time.Hour)).
		Cookie("mapped", "value", map[string]interface{}{
			"path":     "/admin",
			"domain":   "example.com",
			"secure":   true,
			"httponly": true,
			"samesite": "strict",
		}).
		Cookie("legacy", "value", 30, "/legacy", "example.com", true, true)
	cookies := validCookies.GetCookie()
	if len(cookies) != 5 {
		t.Fatalf("Cookie option 未完整生成: %#v err=%v", cookies, validCookies.Error())
	}
	if cookies[3].Path != "/admin" || !cookies[3].Secure || !cookies[3].HttpOnly || cookies[3].SameSite != http.SameSiteStrictMode {
		t.Fatalf("Cookie 映射 option 错误: %#v", cookies[3])
	}
	if cookies[4].Path != "/legacy" || !cookies[4].Secure || !cookies[4].HttpOnly {
		t.Fatalf("旧式 Cookie 参数错误: %#v", cookies[4])
	}

	invalidCookies := NewResponse().
		Cookie("invalid_same_site", "value", map[string]interface{}{"samesite": "unsafe"}).
		Cookie("overflow", "value", uint64(math.MaxUint64)).
		Cookie("wrong_count", "value", 1, 2)
	if !errors.Is(invalidCookies.Error(), ErrInvalidResponseHeader) || len(invalidCookies.GetCookie()) != 0 {
		t.Fatalf("非法 Cookie option 必须拒绝: cookies=%#v err=%v", invalidCookies.GetCookie(), invalidCookies.Error())
	}

	dataTests := []struct {
		name     string
		value    interface{}
		expected string
	}{
		{name: "nil", value: nil, expected: ""},
		{name: "string", value: "content", expected: "content"},
		{name: "bytes", value: []byte("bytes"), expected: "bytes"},
		{name: "bool", value: true, expected: "true"},
		{name: "int", value: 7, expected: "7"},
		{name: "uint", value: uint64(8), expected: "8"},
		{name: "float", value: 1.5, expected: "1.5"},
	}
	for _, test := range dataTests {
		t.Run(test.name, func(t *testing.T) {
			current := NewResponse().Data(test.value)
			if current.GetContent() != test.expected || current.Error() != nil {
				t.Fatalf("Data(%T)=%q err=%v，期望 %q", test.value, current.GetContent(), current.Error(), test.expected)
			}
		})
	}
	unsupported := NewResponse().Data(struct{}{})
	if !errors.Is(unsupported.Error(), ErrResponseSerialization) {
		t.Fatalf("不支持的 HTML 数据类型必须返回序列化错误: %v", unsupported.Error())
	}

	committed := NewCommittedResponse(http.StatusNoContent)
	if !committed.Committed() || committed.GetStatus() != http.StatusNoContent || committed.Error() != nil {
		t.Fatalf("已提交响应状态错误: committed=%t status=%d err=%v", committed.Committed(), committed.GetStatus(), committed.Error())
	}
	invalidCommitted := NewCommittedResponse(http.StatusContinue)
	if invalidCommitted.Committed() || !errors.Is(invalidCommitted.Error(), ErrInvalidResponseStatus) {
		t.Fatalf("临时状态码不得构造为已提交响应: committed=%t err=%v", invalidCommitted.Committed(), invalidCommitted.Error())
	}
	if (*Response)(nil).Committed() {
		t.Fatal("空响应不得报告为已提交")
	}
}

func newThinkPHPRequestSession(t *testing.T) (*frameworksession.Session, func()) {
	t.Helper()
	cookieFactory, err := frameworkcookie.NewCookie(map[string]interface{}{})
	if err != nil {
		t.Fatalf("创建 Cookie 工厂失败: %v", err)
	}
	manager, err := frameworksession.NewSession(map[string]interface{}{}, sessiondriver.NewMemory(), cookieFactory)
	if err != nil {
		t.Fatalf("创建 Session 管理器失败: %v", err)
	}
	requestSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodPost, "http://example.com/", nil), nil)
	if err != nil {
		_ = manager.Close()
		t.Fatalf("创建请求 Session 失败: %v", err)
	}
	return requestSession, func() {
		if closeErr := manager.Close(); closeErr != nil {
			t.Errorf("关闭 Session 管理器失败: %v", closeErr)
		}
	}
}
