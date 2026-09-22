package context

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	frameworkenv "github.com/zhuhanxin0308/thinkgo/framework/env"
)

// TestRequestCoreReadsKeepCompatibilityLazy 验证常规请求及环境注入不会创建未使用的兼容状态。
func TestRequestCoreReadsKeepCompatibilityLazy(t *testing.T) {
	raw := httptest.NewRequest(http.MethodGet, "/health?lang=zh-cn", nil)
	raw.Header.Set("Accept", "application/json")
	raw.AddCookie(&http.Cookie{Name: "language", Value: "zh-cn"})
	environment := frameworkenv.NewEnv()
	request, err := NewRequest(raw, WithEnvService(environment))
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Parse(); err != nil {
		t.Fatal(err)
	}
	if request.Get("lang") != "zh-cn" || request.Header("Accept") != "application/json" || request.Cookie("language") != "zh-cn" {
		t.Fatal("惰性状态改变了普通请求读取")
	}
	if request.Env("missing", "fallback") != "fallback" {
		t.Fatal("惰性状态丢失了环境服务")
	}
	if request.peekThinkPHPState() != nil {
		t.Fatal("普通读取不应创建兼容状态")
	}
	request.WithGet(map[string]interface{}{"lang": "en"})
	if request.Get("lang") != "en" || request.Env("missing", "fallback") != "fallback" {
		t.Fatal("首次创建兼容状态丢失了覆盖值或环境服务")
	}
}

// TestRequestLazyStateConcurrentPublication 保证并发首次写入不同兼容字段时不丢失状态。
func TestRequestLazyStateConcurrentPublication(t *testing.T) {
	request := newRequestForTest(t, httptest.NewRequest(http.MethodGet, "/", nil))
	start := make(chan struct{})
	var group sync.WaitGroup
	for _, change := range []func(){
		func() { request.WithGet(map[string]interface{}{"name": "alice"}) },
		func() { request.WithCookie(map[string]interface{}{"language": "zh-cn"}) },
		func() { request.WithHeader(map[string]string{"Accept": "application/custom"}) },
		func() { request.MimeType("json", "application/custom") },
	} {
		group.Go(func() { <-start; change() })
	}
	close(start)
	group.Wait()
	if request.Get("name") != "alice" || request.Cookie("language") != "zh-cn" || request.Type() != "json" {
		t.Fatal("并发初始化丢失兼容字段")
	}
}

// TestRequestValidateBodyPreservesStrictBoundary 保证延迟构树仍在业务前拒绝全部非法输入。
func TestRequestValidateBodyPreservesStrictBoundary(t *testing.T) {
	for _, body := range []string{"", `[]`, `null`, `{"a":1,"\u0061":2}`, `{"nested":[{"x":1,"x":2}]}`, `{} {}`, `{"a":`, strings.Repeat(`{"x":`, maxJSONNestingDepth+1) + "0" + strings.Repeat("}", maxJSONNestingDepth+1)} {
		t.Run(body, func(t *testing.T) {
			request := newJSONValidationRequest(body)
			if err := request.ValidateBody(); !errors.Is(err, ErrInvalidJSONBody) {
				t.Fatalf("严格边界未拒绝输入: %v", err)
			}
			if err := request.Parse(); !errors.Is(err, ErrInvalidJSONBody) {
				t.Fatalf("参数访问未保留边界错误: %v", err)
			}
			if request.jsonBody != nil {
				t.Fatal("无效输入泄露了部分参数树")
			}
		})
	}
}

// TestRequestValidateBodyDefersTreeAndRestoresRaw 保留原文绑定、自定义解码和按需参数快照。
func TestRequestValidateBodyDefersTreeAndRestoresRaw(t *testing.T) {
	const body = `{"profile":{"name":"alice"},"amount":18446744073709551616}`
	request := newJSONValidationRequest(body)
	if err := request.ValidateBody(); err != nil {
		t.Fatal(err)
	}
	if request.jsonBody != nil {
		t.Fatal("只做边界校验不应构造参数树")
	}
	raw, err := io.ReadAll(request.Raw().Body)
	if err != nil || string(raw) != body {
		t.Fatalf("未恢复原始请求正文: body=%q err=%v", raw, err)
	}
	var custom strictJSONCustomValue
	if err := request.Json(&custom); err != nil || custom.Raw != body || request.jsonBody != nil {
		t.Fatalf("原文绑定改变或提前构树: value=%q err=%v", custom.Raw, err)
	}
	if err := request.Parse(); err != nil || request.jsonBody["amount"] != json.Number("18446744073709551616") {
		t.Fatalf("按需参数树丢失精确数字: err=%v", err)
	}
	sources, err := request.Sources()
	if err != nil {
		t.Fatal(err)
	}
	sources.Body["profile"].(map[string]any)["name"] = "changed"
	if request.All()["profile"].(map[string]any)["name"] != "alice" {
		t.Fatal("外部修改污染了参数树")
	}
}

// TestRequestValidateBodyOverrideVersion 保证校验缓存始终对应当前覆盖输入和媒体类型。
func TestRequestValidateBodyOverrideVersion(t *testing.T) {
	request := newJSONValidationRequest(`{"original":true}`)
	request.WithInput(`{"current":1}`)
	if err := request.ValidateBody(); err != nil || request.thinkPHPState().inputValues != nil {
		t.Fatalf("覆盖输入提前构树或校验失败: %v", err)
	}
	if err := request.ParseError(); err != nil || request.thinkPHPState().inputValues != nil {
		t.Fatalf("错误查询不应构造覆盖输入的值树: %v", err)
	}
	request.WithInput(`{"bad":1,"bad":2}`)
	if err := request.ValidateBody(); !errors.Is(err, ErrInvalidJSONBody) {
		t.Fatalf("新输入复用了旧校验: %v", err)
	}
	request.WithInput(`{"next":2}`)
	if err := request.ValidateBody(); err != nil || request.ParamInt("next", 0) != 2 || request.Has("current") {
		t.Fatalf("有效替换未恢复参数访问: %v", err)
	}
}
