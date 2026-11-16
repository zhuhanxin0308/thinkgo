package framework

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// TestCreateAppSecurityHeadersStrictConfiguration 验证启用、安全默认值、未知字段和类型错误契约。
func TestCreateAppSecurityHeadersStrictConfiguration(t *testing.T) {
	handler, enabled, err := createAppSecurityHeaders(map[string]interface{}{"enable": true})
	if err != nil || !enabled || handler == nil {
		t.Fatalf("启用安全响应头失败: enabled=%t handler=%v err=%v", enabled, handler, err)
	}
	if _, _, err := createAppSecurityHeaders(map[string]interface{}{"enable": true, "unknown": true}); err == nil {
		t.Fatal("未知安全响应头字段必须被拒绝")
	}
	if _, _, err := createAppSecurityHeaders(map[string]interface{}{"enable": "true"}); err == nil {
		t.Fatal("错误 enable 类型必须被拒绝")
	}
	if _, _, err := createAppSecurityHeaders(map[string]interface{}{
		"enable":               true,
		"hsts_preload":         true,
		"hsts_max_age_seconds": 1,
	}); !errors.Is(err, middleware.ErrInvalidSecurityHeaders) {
		t.Fatalf("不安全 HSTS preload 必须被拒绝，实际为 %v", err)
	}
}

func TestSecurityHeadersAcceptPlatformIntegerRangeFromJSON(t *testing.T) {
	const key = "hsts_max_age_seconds"
	age := strconv.Itoa(math.MaxInt)
	for _, value := range []interface{}{json.Number(age), int64(math.MaxInt)} {
		handler, _, err := createAppSecurityHeaders(map[string]interface{}{"enable": true, key: value})
		if err != nil {
			t.Fatalf("平台 int 上限应通过安全头配置解析: %v", err)
		}
		req := context.MustNewRequest(httptest.NewRequest(http.MethodGet, "https://example.test/", nil))
		response := handler(req, func(*context.Request) *context.Response {
			return context.NewResponse()
		})
		if got := response.Headers().Get("Strict-Transport-Security"); got != "max-age="+age {
			t.Fatalf("HSTS max-age 未精确保留: %q", got)
		}
	}
	for _, value := range []json.Number{"1.5", "1e3", json.Number(strconv.FormatUint(uint64(math.MaxInt)+1, 10))} {
		if _, _, err := createAppSecurityHeaders(map[string]interface{}{"enable": true, key: value}); err == nil {
			t.Fatalf("非法或越界 HSTS 整数 %q 应被拒绝", value)
		}
	}
}

func TestProductionBrowserHSTSRespectsExplicitConfiguration(t *testing.T) {
	const key = "hsts_max_age_seconds"
	for _, test := range []struct {
		values map[string]any
		want   string
	}{
		{values: map[string]any{key: 0}},
		{values: map[string]any{key: 600}, want: "max-age=600"},
		{values: map[string]any{key: 600, "hsts_include_subdomains": true}, want: "max-age=600; includeSubDomains"},
	} {
		handler, enabled, err := createAppSecurityHeadersForProfile(test.values, SecurityProfileBrowserCookie, true)
		if err != nil || !enabled {
			t.Fatalf("配置生产安全响应头失败: %v", err)
		}
		req := context.MustNewRequest(httptest.NewRequest(http.MethodGet, "https://example.test/", nil))
		response := handler(req, func(*context.Request) *context.Response { return context.NewResponse() })
		if got := response.Headers().Get("Strict-Transport-Security"); got != test.want {
			t.Fatalf("HSTS 显式配置得到 %q，期望 %q", got, test.want)
		}
	}
	if handler, enabled, err := createAppSecurityHeadersForProfile(map[string]any{"enable": false}, SecurityProfileBrowserCookie, true); err != nil || enabled || handler != nil {
		t.Fatalf("显式关闭安全头必须生效: enabled=%t error=%v", enabled, err)
	}
}
