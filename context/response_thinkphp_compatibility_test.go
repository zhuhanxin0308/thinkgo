package context

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

type thinkPHPResponseStringer struct{}

func (thinkPHPResponseStringer) String() string { return "stringer-content" }

// TestResponseThinkPHPBaseAPI 验证 think.Response 的常用链式 API、默认状态和缓存语义。
func TestResponseThinkPHPBaseAPI(t *testing.T) {
	response := NewResponse()
	if response.GetCode() != http.StatusOK || response.GetStatus() != http.StatusOK {
		t.Fatalf("默认响应状态错误: code=%d status=%d", response.GetCode(), response.GetStatus())
	}
	if !response.IsAllowCache() {
		t.Fatal("ThinkPHP 默认允许请求缓存")
	}
	if response.GetHeader("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("默认响应类型错误: %q", response.GetHeader("Content-Type"))
	}
	if response.Options(map[string]interface{}{"escape_html": false}).AllowCache(false).Data("hello") != response {
		t.Fatal("Response 链式 API 必须返回当前实例")
	}
	if response.IsAllowCache() || response.GetData() != "hello" || response.GetContent() != "hello" {
		t.Fatalf("响应数据或缓存语义错误: allow=%t data=%#v content=%q", response.IsAllowCache(), response.GetData(), response.GetContent())
	}
	if response.Code(http.StatusCreated).GetCode() != http.StatusCreated {
		t.Fatalf("Code/GetCode 不一致: %d", response.GetCode())
	}
}

// TestResponseThinkPHPDataTracksTypedPayload 验证 JSON 响应保留原始数据并输出序列化内容。
func TestResponseThinkPHPDataTracksTypedPayload(t *testing.T) {
	payload := map[string]interface{}{"ok": true}
	response := NewResponse().Json(payload)
	if response.GetData() == nil || response.GetContent() != `{"ok":true}` {
		t.Fatalf("JSON 原始数据或内容错误: data=%#v content=%q", response.GetData(), response.GetContent())
	}
}

// TestResponseThinkPHPContentAcceptsScalarValues 验证 content 接受字符串、
// 数字、nil 和 Stringer，并拒绝 ThinkPHP 不支持的复合类型。
func TestResponseThinkPHPContentAcceptsScalarValues(t *testing.T) {
	response := NewResponse().Content(42)
	if response.GetContent() != "42" {
		t.Fatalf("数字内容转换错误: %q", response.GetContent())
	}
	response.Content(thinkPHPResponseStringer{})
	if response.GetContent() != "stringer-content" {
		t.Fatalf("Stringer 内容转换错误: %q", response.GetContent())
	}
	response.Content(nil)
	if response.GetContent() != "" {
		t.Fatalf("nil 内容应转换为空字符串: %q", response.GetContent())
	}
	response.Content("safe").Content(map[string]string{"invalid": "content"})
	if !errors.Is(response.Error(), ErrResponseSerialization) || response.GetContent() != "safe" {
		t.Fatalf("非法内容必须保留原响应并记录错误: content=%q err=%v", response.GetContent(), response.Error())
	}
}

// TestResponseThinkPHPHeaderAndCookieCallingStyle 验证 header 使用数组式批量设置，
// cookie 只传名称和值即可采用 ThinkPHP 默认配置，同时仍支持 option 覆盖。
func TestResponseThinkPHPHeaderAndCookieCallingStyle(t *testing.T) {
	response := NewResponse().
		Header(map[string]string{"X-Framework": "ThinkGo", "X-Version": "8"}).
		Cookie("session", "default").
		Cookie("token", "secure", map[string]interface{}{
			"expire":   3600,
			"path":     "/api",
			"domain":   "example.com",
			"secure":   true,
			"httponly": true,
			"samesite": "strict",
		})
	if response.GetHeader("X-Framework") != "ThinkGo" || response.GetHeader("X-Version") != "8" {
		t.Fatalf("批量响应头设置错误: %#v", response.Headers())
	}
	cookies := response.Headers().Values("Set-Cookie")
	if len(cookies) != 2 {
		t.Fatalf("应写入两个 Cookie，实际为 %#v", cookies)
	}
	defaultCookie, err := http.ParseSetCookie(cookies[0])
	if err != nil {
		t.Fatalf("解析默认 Cookie 失败: %v", err)
	}
	if defaultCookie.Path != "/" || defaultCookie.Secure || defaultCookie.HttpOnly || !defaultCookie.Expires.IsZero() {
		t.Fatalf("默认 Cookie 配置不符合 ThinkPHP: %#v", defaultCookie)
	}
	secureCookie, err := http.ParseSetCookie(cookies[1])
	if err != nil {
		t.Fatalf("解析覆盖 Cookie 失败: %v", err)
	}
	if secureCookie.Path != "/api" || secureCookie.Domain != "example.com" || !secureCookie.Secure || !secureCookie.HttpOnly || secureCookie.SameSite != http.SameSiteStrictMode || secureCookie.Expires.IsZero() {
		t.Fatalf("Cookie option 覆盖错误: %#v", secureCookie)
	}
	if !strings.Contains(cookies[1], "SameSite=Strict") {
		t.Fatalf("Cookie SameSite 输出错误: %q", cookies[1])
	}
}
