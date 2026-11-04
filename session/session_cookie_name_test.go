package session

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
)

func TestSessionCookieNameUsesCookiePrefix(t *testing.T) {
	for _, prefix := range []string{"", "tenant_"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			manager := newTestSessionManager(t, newCountingDriver(), map[string]interface{}{"name": "SID"}, map[string]interface{}{"prefix": prefix})
			requestSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "http://example.com", nil), nil)
			if err != nil {
				t.Fatalf("创建请求级 Session 失败: %v", err)
			}
			for _, instance := range []*Session{manager, requestSession} {
				name, err := instance.CookieName()
				if err != nil || name != prefix+"SID" {
					t.Fatalf("管理器与请求实例应解析同一 Cookie 名称: name=%q err=%v", name, err)
				}
				if configured := instance.GetConfig().Name; configured != "SID" {
					t.Fatalf("解析实际名称不得修改逻辑配置名称: %q", configured)
				}
			}
		})
	}
}

func TestSessionCookieNamePreservesValidationErrors(t *testing.T) {
	for _, instance := range []*Session{nil, &Session{}} {
		if name, err := instance.CookieName(); name != "" || !errors.Is(err, ErrInvalidSessionDependency) {
			t.Fatalf("缺少 Cookie 依赖时必须显式失败: name=%q err=%v", name, err)
		}
	}
	manager := newTestSessionManager(t, newCountingDriver(), map[string]interface{}{
		"name": strings.Repeat("s", maxSessionNameBytes),
	}, map[string]interface{}{
		"prefix": strings.Repeat("p", 129),
	})
	if name, err := manager.CookieName(); name != "" || !errors.Is(err, ErrSessionCookie) || !errors.Is(err, cookie.ErrInvalidCookieName) {
		t.Fatalf("合并后名称过长时必须保留 Cookie 错误链: name=%q err=%v", name, err)
	}
}
