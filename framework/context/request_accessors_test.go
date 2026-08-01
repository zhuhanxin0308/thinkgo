package context

import (
	stdcontext "context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestRequestMetadataAccessors 验证请求元数据、绝对地址和兼容别名保持一致语义。
func TestRequestMetadataAccessors(t *testing.T) {
	requestContext := stdcontext.WithValue(stdcontext.Background(), struct{ name string }{"request"}, "context-value")
	raw := httptest.NewRequest(http.MethodGet, "https://example.com:8443/files/report.json?q=go", nil).WithContext(requestContext)
	raw.Header.Set("X-Requested-With", "XMLHttpRequest")
	raw.Header.Set("X-Empty", "")
	raw.AddCookie(&http.Cookie{Name: "sid", Value: "abc"})
	req := newRequestForTest(t, raw)

	if req.Method() != http.MethodGet || req.Host() != "example.com:8443" || req.Path() != "/files/report.json" {
		t.Fatalf("基础元数据错误: method=%q host=%q path=%q", req.Method(), req.Host(), req.Path())
	}
	if req.Pathinfo() != req.Path() || req.Ext() != "json" {
		t.Fatalf("路径别名或扩展名错误: pathinfo=%q ext=%q", req.Pathinfo(), req.Ext())
	}
	if req.Url() != "/files/report.json?q=go" || req.Url(true) != "https://example.com:8443/files/report.json?q=go" {
		t.Fatalf("URL 辅助结果错误: relative=%q complete=%q", req.Url(), req.Url(true))
	}
	if req.BaseUrl() != "https://example.com:8443/files/report.json" || req.Root() != "https://example.com:8443" {
		t.Fatalf("绝对 URL 辅助结果错误: base=%q root=%q", req.BaseUrl(), req.Root())
	}
	if !req.IsGet() || req.IsPost() || req.IsPut() || req.IsDelete() || !req.IsAjax() || !req.IsSsl() {
		t.Fatal("请求方法、AJAX 或 TLS 判断错误")
	}
	if req.Scheme() != "https" || req.Port() != "8443" || req.Cookie("sid") != "abc" || req.Cookie("missing") != "" {
		t.Fatalf("协议、端口或 Cookie 错误: scheme=%q port=%q", req.Scheme(), req.Port())
	}
	if req.Header("X-Empty", "fallback") != "" || req.Header("X-Missing", "fallback") != "fallback" {
		t.Fatal("请求头显式空值与缺失值语义错误")
	}
	if req.ContentType() != "application/x-www-form-urlencoded" || req.Server("X-Requested-With") != "XMLHttpRequest" {
		t.Fatal("ContentType 默认值或 Server 兼容别名错误")
	}
	mediaType, err := req.MediaType()
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		t.Fatalf("媒体类型解析错误: type=%q err=%v", mediaType, err)
	}
	if req.Raw() != raw {
		t.Fatal("Raw 应返回当前原生请求")
	}
	if req.Context().Value(struct{ name string }{"request"}) != "context-value" {
		t.Fatal("Context 应返回原生请求上下文")
	}

	methods := map[string]func(*Request) bool{
		http.MethodPost:   (*Request).IsPost,
		http.MethodPut:    (*Request).IsPut,
		http.MethodDelete: (*Request).IsDelete,
	}
	for method, predicate := range methods {
		methodReq := newRequestForTest(t, httptest.NewRequest(method, "/", nil))
		if !predicate(methodReq) {
			t.Fatalf("方法判断 %s 应返回 true", method)
		}
	}
}

// TestRequestContextForNilRequest 验证空 Request 仍可安全获取后台上下文。
func TestRequestContextForNilRequest(t *testing.T) {
	var request *Request
	if request.Context() == nil {
		t.Fatal("空 Request.Context 不应返回 nil")
	}
	if _, ok := request.Context().Deadline(); ok {
		t.Fatal("后台上下文不应伪造截止时间")
	}
}

// TestRequestScalarConversions 验证所有受支持标量类型的格式化、整数、浮点和布尔转换边界。
func TestRequestScalarConversions(t *testing.T) {
	req := newRequestForTest(t, httptest.NewRequest(http.MethodGet, "/", nil))
	integerValues := map[string]struct {
		value    interface{}
		expected int64
	}{
		"int": {value: int(1), expected: 1}, "int8": {value: int8(2), expected: 2},
		"int16": {value: int16(3), expected: 3}, "int32": {value: int32(4), expected: 4},
		"int64": {value: int64(5), expected: 5}, "uint": {value: uint(6), expected: 6},
		"uint8": {value: uint8(7), expected: 7}, "uint16": {value: uint16(8), expected: 8},
		"uint32": {value: uint32(9), expected: 9}, "uint64": {value: uint64(10), expected: 10},
		"float32": {value: float32(11), expected: 11}, "float64": {value: float64(12), expected: 12},
		"number": {value: json.Number("1e2"), expected: 100}, "string": {value: "13", expected: 13},
	}
	for key, test := range integerValues {
		req.Set(key, test.value)
		if got := req.ParamInt64(key, -1); got != test.expected {
			t.Fatalf("%s 转 int64 错误，期望 %d，实际 %d", key, test.expected, got)
		}
		if req.Param(key, "missing") == "missing" {
			t.Fatalf("%s 标量格式化不应回退默认值", key)
		}
	}
	req.Set("too_large", uint64(math.MaxUint64))
	req.Set("fraction", 1.5)
	if req.ParamInt64("too_large", 7) != 7 || req.ParamInt("fraction", 8) != 8 {
		t.Fatal("溢出或小数整数转换必须回退默认值")
	}
	if req.ParamFloat("number", -1) != 100 || req.ParamFloat("fraction", -1) != 1.5 {
		t.Fatal("有限浮点转换错误")
	}

	booleanValues := map[string]struct {
		value    interface{}
		expected bool
	}{
		"bool": {value: true, expected: true}, "one": {value: 1, expected: true},
		"zero": {value: float64(0), expected: false}, "yes": {value: "yes", expected: true},
		"off": {value: "OFF", expected: false},
	}
	for key, test := range booleanValues {
		req.Set(key, test.value)
		if got := req.ParamBool(key, !test.expected); got != test.expected {
			t.Fatalf("%s 转 bool 错误，期望 %v，实际 %v", key, test.expected, got)
		}
	}
	if req.ParamBool("missing", true) != true || req.ParamFloat("missing", 2.5) != 2.5 {
		t.Fatal("缺失类型参数必须返回默认值")
	}
}

// TestRequestJSONArraysAndBinding 验证 JSON 数组标量化、严格绑定和嵌套快照。
func TestRequestJSONArraysAndBinding(t *testing.T) {
	body := `{"items":["a",2,true],"bad":[{"id":1}],"profile":{"name":"alice"}}`
	raw := httptest.NewRequest(http.MethodPost, "/json", strings.NewReader(body))
	raw.Header.Set("Content-Type", "application/problem+json")
	req := newRequestForTest(t, raw)
	if err := req.Parse(); err != nil {
		t.Fatalf("解析合法 +json 请求失败: %v", err)
	}
	if got := req.PostArray("items"); !reflect.DeepEqual(got, []string{"a", "2", "true"}) {
		t.Fatalf("JSON 标量数组转换错误: %#v", got)
	}
	if req.PostArray("bad") != nil || req.PostArray("missing") != nil {
		t.Fatal("复杂或缺失 JSON 数组应返回 nil")
	}
	var target struct {
		Items []interface{} `json:"items"`
	}
	if err := req.Json(&target); err != nil || len(target.Items) != 3 {
		t.Fatalf("严格 JSON 绑定失败: target=%#v err=%v", target, err)
	}
	if err := req.Json(nil); err == nil {
		t.Fatal("空 JSON 绑定目标必须返回错误")
	}
	if req.ParseError() != nil || req.JSONError() != nil {
		t.Fatalf("合法 JSON 不应保留解析错误: %v", req.ParseError())
	}
}

// TestRequestZeroValueAndNilRaw 验证零值 Request 与空原生请求不会 panic。
func TestRequestZeroValueAndNilRaw(t *testing.T) {
	var zero Request
	zero.Set("key", "value")
	if zero.GetData("key") != "value" || zero.Input("key") != "value" {
		t.Fatal("零值 Request 的数据读写错误")
	}
	if zero.Method() != "" || zero.Host() != "" || zero.Path() != "" || zero.Raw() != nil {
		t.Fatal("零值 Request 元数据应为空")
	}
	if body, err := zero.Body(); err != nil || len(body) != 0 {
		t.Fatalf("零值 Request Body 应为空: body=%q err=%v", body, err)
	}
	if _, err := zero.File("file"); err == nil {
		t.Fatal("零值 Request 获取文件必须返回错误")
	}
	if err := zero.Cleanup(); err != nil {
		t.Fatalf("零值 Request 清理应幂等: %v", err)
	}
}
