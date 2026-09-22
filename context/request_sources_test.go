package context

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInputReplacementOwnsParseErrors 验证显式替换输入后，所有解析入口都反映新输入的错误状态。
func TestInputReplacementOwnsParseErrors(t *testing.T) {
	raw := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":`))
	raw.Header.Set("Content-Type", "application/json")
	request, err := NewRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Parse(); !errors.Is(err, ErrInvalidJSONBody) {
		t.Fatalf("原始无效 JSON 未被拒绝: %v", err)
	}
	request.WithInput(`{"name":"current"}`)
	if err := request.Parse(); err != nil {
		t.Fatal(err)
	}
	if err := request.ParseError(); err != nil {
		t.Fatalf("仍报告旧输入错误: %v", err)
	}
	if err := request.JSONError(); err != nil {
		t.Fatalf("JSON 错误未更新: %v", err)
	}
	sources, err := request.Sources()
	if err != nil || sources.Body["name"] != "current" {
		t.Fatalf("快照未使用新输入: %#v %v", sources, err)
	}
	request.WithInput(`{"name":`)
	if err := request.ParseError(); !errors.Is(err, ErrInvalidJSONBody) {
		t.Fatalf("新的错误输入没有被报告: %v", err)
	}
}

// TestEmptyFormReplacementKeepsBodyPresence 验证空输入与显式 POST 数据的存在状态不混淆。
func TestEmptyFormReplacementKeepsBodyPresence(t *testing.T) {
	request, err := NewRequest(httptest.NewRequest("POST", "/", strings.NewReader("old=value")))
	if err != nil {
		t.Fatal(err)
	}
	request.WithInput("")
	sources, err := request.Sources()
	if err != nil || sources.HasBody || len(sources.Form) != 0 {
		t.Fatalf("空输入被伪装成有请求体: %#v %v", sources, err)
	}
	request.WithPost(map[string]interface{}{"value": "new"})
	sources, err = request.Sources()
	if err != nil || !sources.HasBody || sources.Form.Get("value") != "new" {
		t.Fatalf("显式数据丢失: %#v %v", sources, err)
	}
}

// TestBodyReadErrorFollowsReplacement 验证替换正文后只报告当前正文的读取边界，不保留旧流的错误。
func TestBodyReadErrorFollowsReplacement(t *testing.T) {
	failure := errors.New("original stream failed")
	request := newRequestForTest(t, httptest.NewRequest("POST", "/", failingBodyReader{err: failure}), WithMaxBodyBytes(4))
	if _, err := request.Body(); !errors.Is(err, failure) {
		t.Fatalf("原始流错误未保留: %v", err)
	}
	request.WithInput("ok")
	if err := request.BodyReadError(); err != nil {
		t.Fatalf("替换后仍报告旧流错误: %v", err)
	}
	request.WithInput("large")
	if err := request.BodyReadError(); !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("替换正文的大小限制未报告: %v", err)
	}
}
