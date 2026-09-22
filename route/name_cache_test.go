package route

import (
	"bytes"
	"testing"
)

// TestNamedRouteCacheSurvivesNewRouter 验证优化产物被新路由器实际消费并保留参数约束、可选段和后缀。
func TestNamedRouteCacheSurvivesNewRouter(t *testing.T) {
	first := NewRouter()
	registered, err := first.Get("accounts/:id/[:tab]", "Account/read")
	if err != nil {
		t.Fatal(err)
	}
	if err := registered.WithName("account.read"); err != nil {
		t.Fatal(err)
	}
	if err := registered.WithPattern("id", `[0-9]+`); err != nil {
		t.Fatal(err)
	}
	if err := registered.WithExtension("json"); err != nil {
		t.Fatal(err)
	}
	content, err := first.ExportNameCache()
	if err != nil {
		t.Fatal(err)
	}
	second := NewRouter()
	if err := second.LoadNameCache(content); err != nil {
		t.Fatal(err)
	}
	for _, params := range []map[string]interface{}{{"id": 12}, {"id": 12, "tab": "profile", "page": 2}} {
		want, err := first.URL("account.read", params)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := second.URL("account.read", params); err != nil || got != want {
			t.Fatalf("优化缓存 URL 不一致: got=%q want=%q err=%v", got, want, err)
		}
	}
	if _, err := second.URL("account.read", map[string]interface{}{"id": "bad"}); err == nil {
		t.Fatal("缓存绕过了路由参数约束")
	}
	again, err := first.ExportNameCache()
	if err != nil || !bytes.Equal(content, again) {
		t.Fatalf("缓存输出不稳定: %v", err)
	}
}

// TestNamedRouteCacheRejectsInvalidEnvelope 验证缓存损坏或恶意元数据不能替换已生效缓存。
func TestNamedRouteCacheRejectsInvalidEnvelope(t *testing.T) {
	router := NewRouter()
	for _, content := range []string{`null`, `{}`, `{"version":9}`, `{"version":1,"entries":[{"name":"bad","path":"/x/:id","patterns":{"id":"["}}]}`, `{"version":1,"entries":[]} {}`} {
		if err := router.LoadNameCache([]byte(content)); err == nil {
			t.Fatalf("非法缓存未被拒绝: %s", content)
		}
	}
}
