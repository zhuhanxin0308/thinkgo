package context

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestApplicationContextCanonicalHostAndPathBoundaries 验证规范 Host 回退和路径拼接的安全边界。
func TestApplicationContextCanonicalHostAndPathBoundaries(t *testing.T) {
	withCanonicalHost := NewApplicationContextWithCanonicalHost("admin", "/", "/", "/admin", "user-host", "configured-host", true)
	if withCanonicalHost.CanonicalHost() != "configured-host" || !withCanonicalHost.DomainBound() {
		t.Fatalf("应优先返回配置的规范 Host: host=%q bound=%t", withCanonicalHost.CanonicalHost(), withCanonicalHost.DomainBound())
	}
	withoutCanonicalHost := NewApplicationContext("admin", "/", "/", "/admin", "fallback-host", false)
	if withoutCanonicalHost.CanonicalHost() != "fallback-host" {
		t.Fatalf("规范 Host 为空时应回退原始 Host: %q", withoutCanonicalHost.CanonicalHost())
	}

	validPaths := map[string]string{
		"":              "/admin/",
		"/users?tag=go": "/admin/users?tag=go",
		"/admin/users":  "/admin/users",
	}
	for path, want := range validPaths {
		got, err := withCanonicalHost.applicationPath(path)
		if err != nil || got != want {
			t.Fatalf("应用路径拼接结果错误: path=%q got=%q want=%q err=%v", path, got, want, err)
		}
	}
	for _, path := range []string{
		"http://evil.example/",
		"/%00",
		"/%5c",
		"/%2e%2e/private",
	} {
		if _, err := withCanonicalHost.applicationPath(path); !errors.Is(err, ErrInvalidApplicationPath) {
			t.Fatalf("危险应用路径应被拒绝: path=%q err=%v", path, err)
		}
	}
	for _, prefix := range []string{"relative", "/contains..", "/contains%escape", "/contains\\slash"} {
		if _, err := NewApplicationContext("admin", "/", "/", prefix, "host", false).applicationPath("/users"); !errors.Is(err, ErrInvalidApplicationPath) {
			t.Fatalf("非法应用前缀应被拒绝: prefix=%q err=%v", prefix, err)
		}
	}
}

// TestResponseNilAndInvalidStreamBoundaries 验证空响应和空流回调不会绕过错误状态。
func TestResponseNilAndInvalidStreamBoundaries(t *testing.T) {
	var nilResponse *Response
	if !errors.Is(nilResponse.Error(), ErrInvalidResponseWriter) {
		t.Fatalf("空响应 Error 应返回响应错误: %v", nilResponse.Error())
	}
	recorder := httptest.NewRecorder()
	response := NewResponse().Stream(nil)
	if !errors.Is(response.Error(), ErrInvalidStream) {
		t.Fatalf("空流回调应记录 ErrInvalidStream: %v", response.Error())
	}
	if err := response.Send(recorder); !errors.Is(err, ErrInvalidStream) {
		t.Fatalf("发送空流响应应返回 ErrInvalidStream: %v", err)
	}
	if recorder.Code != http.StatusInternalServerError || recorder.Body.Len() == 0 {
		t.Fatalf("空流响应应安全降级为通用 500: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

// TestZeroResponseAcceptsValidStream 验证零值响应也能安全配置有效流回调。
func TestZeroResponseAcceptsValidStream(t *testing.T) {
	var zero Response
	if zero.Stream(func(io.Writer) error { return nil }).Error() != nil {
		t.Fatal("零值 Response 设置有效流回调不应因空 Header 失败")
	}
}

type panicResponseError struct{}

func (panicResponseError) Error() string {
	panic("响应传输错误不应泄露底层 panic")
}

// TestResponseTransmissionErrorSafety 验证传输错误包装器在空值和异常 Error 实现下仍保持安全文本。
func TestResponseTransmissionErrorSafety(t *testing.T) {
	var nilTransmission *responseTransmissionError
	if nilTransmission.Error() != "响应传输失败" || nilTransmission.Unwrap() != nil {
		t.Fatalf("空传输错误包装器结果错误: error=%q unwrap=%v", nilTransmission.Error(), nilTransmission.Unwrap())
	}
	wrapped := &responseTransmissionError{err: panicResponseError{}}
	if wrapped.Error() != "响应传输失败" {
		t.Fatalf("底层 Error panic 时应返回通用错误: %q", wrapped.Error())
	}
	underlying := errors.New("transport failure")
	wrapped = &responseTransmissionError{err: underlying}
	if wrapped.Error() != underlying.Error() || !errors.Is(wrapped, underlying) {
		t.Fatalf("传输错误应保留可诊断的 Unwrap: error=%q unwrap=%v", wrapped.Error(), wrapped.Unwrap())
	}
}
