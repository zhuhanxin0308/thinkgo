package context

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRequestThinkPHPURLAndServerAPI 验证 URL、Host、端口和 CGI 变量的
// 默认返回值与本地 ThinkPHP 8.1.4 的 think.Request 一致。
func TestRequestThinkPHPURLAndServerAPI(t *testing.T) {
	raw := httptest.NewRequest(http.MethodGet, "https://api.example.com:8443/files/report.json?q=go", nil)
	raw.RemoteAddr = "192.0.2.18:52100"
	request := newRequestForTest(t, raw)

	if request.Pathinfo() != "files/report.json" {
		t.Fatalf("Pathinfo 默认不应包含开头斜杠: %q", request.Pathinfo())
	}
	if request.Url() != "/files/report.json?q=go" || request.Url(true) != "https://api.example.com:8443/files/report.json?q=go" {
		t.Fatalf("Url 结果错误: relative=%q complete=%q", request.Url(), request.Url(true))
	}
	if request.BaseUrl() != "/files/report.json" || request.BaseUrl(true) != "https://api.example.com:8443/files/report.json" {
		t.Fatalf("BaseUrl 结果错误: relative=%q complete=%q", request.BaseUrl(), request.BaseUrl(true))
	}
	if request.Root() != "" || request.Root(true) != "https://api.example.com:8443" {
		t.Fatalf("Root 结果错误: relative=%q complete=%q", request.Root(), request.Root(true))
	}
	if request.Domain() != "https://api.example.com:8443" || request.Domain(true) != "https://api.example.com" {
		t.Fatalf("Domain 结果错误: domain=%q strict=%q", request.Domain(), request.Domain(true))
	}
	if request.Host() != "api.example.com:8443" || request.Host(true) != "api.example.com" {
		t.Fatalf("Host 结果错误: host=%q strict=%q", request.Host(), request.Host(true))
	}
	if request.Port() != 8443 || request.RemotePort() != 52100 {
		t.Fatalf("端口结果错误: server=%d remote=%d", request.Port(), request.RemotePort())
	}
	if request.Protocol() != "HTTP/1.1" || request.Query() != "q=go" {
		t.Fatalf("协议或查询字符串错误: protocol=%q query=%q", request.Protocol(), request.Query())
	}
	if request.Server("REQUEST_METHOD") != http.MethodGet || request.Server("QUERY_STRING") != "q=go" || request.Server("REMOTE_ADDR") != "192.0.2.18" {
		t.Fatalf("Server CGI 映射错误: method=%q query=%q remote=%q", request.Server("REQUEST_METHOD"), request.Server("QUERY_STRING"), request.Server("REMOTE_ADDR"))
	}
	if request.ContentType() != "" {
		t.Fatalf("缺少 Content-Type 时应返回空字符串: %q", request.ContentType())
	}
}

// TestRequestThinkPHPMethodAndDetectionAPI 验证 ThinkPHP 常用请求判断与覆盖入口。
func TestRequestThinkPHPMethodAndDetectionAPI(t *testing.T) {
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/items?_ajax=1&_pjax=1", nil)
	raw.Header.Set("X-HTTP-Method-Override", http.MethodPatch)
	raw.Header.Set("Accept", "application/json")
	request := newRequestForTest(t, raw)

	if request.Method() != http.MethodPatch || request.Method(true) != http.MethodPost || !request.IsPatch() {
		t.Fatalf("请求方法覆盖错误: method=%q origin=%q", request.Method(), request.Method(true))
	}
	if request.IsPost() || request.IsHead() || request.IsOptions() {
		t.Fatal("请求方法判断结果错误")
	}
	if !request.IsJson() || !request.IsAjax() || !request.IsPjax() {
		t.Fatal("JSON、Ajax 或 Pjax 判断结果错误")
	}
	request.SetMethod(http.MethodDelete)
	if !request.IsDelete() || request.Method(true) != http.MethodPost {
		t.Fatalf("SetMethod 不应改变原始方法: method=%q origin=%q", request.Method(), request.Method(true))
	}
}

// TestRequestThinkPHPURLSetters 验证 Request.php 暴露的链式 URL 设置 API。
func TestRequestThinkPHPURLSetters(t *testing.T) {
	request := newRequestForTest(t, httptest.NewRequest(http.MethodGet, "http://example.com/original?q=1", nil))
	if request.SetUrl("/changed?x=2") != request || request.SetBaseUrl("/changed") != request || request.SetRoot("/index") != request || request.SetPathinfo("changed/item.html") != request || request.SetHost("api.example.com:9443") != request || request.SetDomain("https://api.example.com:9443") != request {
		t.Fatal("请求设置 API 必须支持链式调用")
	}
	if request.Url() != "/changed?x=2" || request.BaseUrl() != "/changed" || request.Root() != "/index" || request.Pathinfo() != "changed/item.html" || request.Host() != "api.example.com:9443" || request.Domain() != "https://api.example.com:9443" {
		t.Fatalf("请求设置 API 未生效: url=%q base=%q root=%q pathinfo=%q host=%q domain=%q", request.Url(), request.BaseUrl(), request.Root(), request.Pathinfo(), request.Host(), request.Domain())
	}
}

// TestRequestThinkPHPDispatchMetadataAPI 验证控制器分层、控制器和动作元数据
// 与 ThinkPHP Request 的链式设置、大小写转换和 basename 行为一致。
func TestRequestThinkPHPDispatchMetadataAPI(t *testing.T) {
	request := newRequestForTest(t, httptest.NewRequest(http.MethodGet, "http://example.com/admin/user/save", nil))
	if request.SetLayer("Admin").SetController("Admin.User").SetAction("saveProfile") != request {
		t.Fatal("请求调度元数据设置 API 必须支持链式调用")
	}
	if request.Layer() != "Admin" || request.Layer(true) != "admin" {
		t.Fatalf("Layer 转换错误: origin=%q lower=%q", request.Layer(), request.Layer(true))
	}
	if request.Controller() != "Admin.User" || request.Controller(true) != "admin.user" {
		t.Fatalf("Controller 转换错误: origin=%q lower=%q", request.Controller(), request.Controller(true))
	}
	if request.Controller(false, true) != "User" || request.Controller(true, true) != "user" {
		t.Fatalf("Controller basename 转换错误: base=%q lower_base=%q", request.Controller(false, true), request.Controller(true, true))
	}
	if request.Action() != "saveProfile" || request.Action(true) != "saveprofile" {
		t.Fatalf("Action 转换错误: origin=%q lower=%q", request.Action(), request.Action(true))
	}
}

// TestRequestThinkPHPBodyInputAPI 验证 PUT、DELETE、PATCH、request 和原始输入
// 的名称、默认值及解析行为与 ThinkPHP Request 保持一致。
func TestRequestThinkPHPBodyInputAPI(t *testing.T) {
	raw := httptest.NewRequest(http.MethodPut, "http://example.com/users?source=query", strings.NewReader(`{"name":"alice","source":"body"}`))
	raw.Header.Set("Content-Type", "application/json; charset=utf-8")
	request := newRequestForTest(t, raw)

	if request.Put("name") != "alice" || request.Delete("name") != "alice" || request.Patch("name") != "alice" {
		t.Fatalf("PUT 系列参数解析错误: put=%q delete=%q patch=%q", request.Put("name"), request.Delete("name"), request.Patch("name"))
	}
	if request.Put("missing", "fallback") != "fallback" {
		t.Fatalf("PUT 缺失参数默认值错误: %q", request.Put("missing", "fallback"))
	}
	if request.Request("source") != "body" {
		t.Fatalf("Request 应采用 Param 的合并优先级，实际为 %q", request.Request("source"))
	}
	wantInput := `{"name":"alice","source":"body"}`
	if request.GetContent() != wantInput || request.GetInput() != wantInput {
		t.Fatalf("原始输入读取错误: content=%q input=%q", request.GetContent(), request.GetInput())
	}
}
