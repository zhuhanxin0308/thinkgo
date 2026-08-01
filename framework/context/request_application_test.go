package context

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestApplicationContextIsScopedAndBuildsApplicationPath(t *testing.T) {
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/admin/users?tag=go", nil)
	req := MustNewRequest(raw)
	application := NewApplicationContext("admin", "/admin/users", "/users", "/admin", "example.com", false)
	req.SetApplicationContext(application)

	got, ok := req.ApplicationContext()
	if !ok {
		t.Fatal("请求应该包含应用上下文")
	}
	if got.Name() != "admin" || got.OriginalPath() != "/admin/users" || got.RewrittenPath() != "/users" || got.PathPrefix() != "/admin" || got.Host() != "example.com" || got.DomainBound() {
		t.Fatalf("应用上下文读取结果错误: %#v", got)
	}

	applicationPath, err := req.ApplicationPath("/users?tag=go")
	if err != nil {
		t.Fatalf("生成应用路径失败: %v", err)
	}
	if applicationPath != "/admin/users?tag=go" {
		t.Fatalf("应用路径前缀错误，实际为 %q", applicationPath)
	}
	if raw.URL.Path != "/admin/users" || raw.URL.RawQuery != "tag=go" {
		t.Fatalf("生成应用路径不能修改原始请求，path=%q query=%q", raw.URL.Path, raw.URL.RawQuery)
	}

	if _, err = req.ApplicationPath("/users\r\nX-Test: forged"); !errors.Is(err, ErrInvalidApplicationPath) {
		t.Fatalf("非法应用路径必须返回 ErrInvalidApplicationPath，实际为 %v", err)
	}
}

func TestRequestApplicationPathKeepsDomainBoundPathUnprefixed(t *testing.T) {
	req := MustNewRequest(httptest.NewRequest(http.MethodGet, "https://admin.example.com/users", nil))
	req.SetApplicationContext(NewApplicationContext("admin", "/users", "/users", "", "admin.example.com", true))

	path, err := req.ApplicationPath("/users")
	if err != nil {
		t.Fatalf("生成域名绑定应用路径失败: %v", err)
	}
	if path != "/users" {
		t.Fatalf("域名绑定应用不应该追加应用前缀，实际为 %q", path)
	}
}
