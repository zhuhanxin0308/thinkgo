package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"thinkgo/framework/context"
)

// TestRequestIDPreservesCallerValue 验证调用方提供的追踪号会贯穿请求和响应。
func TestRequestIDPreservesCallerValue(t *testing.T) {
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	raw.Header.Set("X-Request-ID", " request-123 ")
	req := context.MustNewRequest(raw)

	resp := RequestID(req, func(got *context.Request) *context.Response {
		if got.GetData(RequestIDKey) != "request-123" {
			t.Fatalf("请求上下文中的追踪号错误: %#v", got.GetData(RequestIDKey))
		}
		return context.NewResponse()
	})
	if resp == nil || resp.Headers().Get("X-Request-ID") != "request-123" {
		t.Fatalf("响应追踪号未写入: %#v", resp)
	}
}

// TestRequestIDRejectsUnboundedOrControlValue 验证不可信追踪号不会进入上下文和响应。
func TestRequestIDRejectsUnboundedOrControlValue(t *testing.T) {
	for _, input := range []string{"bad\nvalue", strings.Repeat("x", maxRequestIDLength+1)} {
		raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		raw.Header.Set("X-Request-ID", input)
		req := context.MustNewRequest(raw)
		resp := RequestID(req, func(got *context.Request) *context.Response {
			value, ok := got.GetData(RequestIDKey).(string)
			if !ok || value == input || len(value) != 36 {
				t.Fatalf("不安全追踪号应被替换为 UUID: %#v", got.GetData(RequestIDKey))
			}
			return context.NewResponse()
		})
		if resp.Headers().Get("X-Request-ID") == input {
			t.Fatalf("不安全追踪号不应回显: %q", input)
		}
	}
}

// TestRequestIDGeneratesValueAndNormalizesNilResponse 验证缺少追踪号时生成 UUID，
// 且下游返回 nil 不会让中间件输出 nil 响应。
func TestRequestIDGeneratesValueAndNormalizesNilResponse(t *testing.T) {
	req := context.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	resp := RequestID(req, func(got *context.Request) *context.Response {
		value, ok := got.GetData(RequestIDKey).(string)
		if !ok || len(value) != 36 {
			t.Fatalf("应生成标准 UUID 追踪号: %#v", got.GetData(RequestIDKey))
		}
		return nil
	})
	if resp == nil {
		t.Fatal("下游返回 nil 时中间件必须返回空响应")
	}
	if value := resp.Headers().Get("X-Request-ID"); len(value) != 36 {
		t.Fatalf("生成的响应追踪号格式错误: %q", value)
	}
}
